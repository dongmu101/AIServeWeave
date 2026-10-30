# Install and maintain the native Agent as the current Windows user.
# 以当前 Windows 用户安装及维护原生 Agent。
[CmdletBinding()]
param(
    [Parameter(Mandatory=$true,Position=0)]
    [ValidateSet('install','upgrade','rollback','start','stop','status','reload','uninstall')][string]$Action,
    [string]$Version, [string]$File, [string]$Checksums, [string]$BaseUrl,
    [string]$NodeId, [string]$Registry, [string]$Gateway,
    [string]$CAFile, [string]$TokenFile,
    [string]$Root = (Join-Path $env:LOCALAPPDATA 'AIServeWeave\Agent'),
    [string]$CliPath, [switch]$Stage
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$sid = $identity.User.Value
$principal = New-Object Security.Principal.WindowsPrincipal($identity)
if (!$Stage -and $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { throw 'Run as the intended ordinary user, without elevation.' }
$build = [int](Get-ItemProperty 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion').CurrentBuildNumber
$product = (Get-ItemProperty 'HKLM:\SYSTEM\CurrentControlSet\Control\ProductOptions').ProductType
if (!$Stage -and ($build -lt 22000 -or $product -ne 'WinNT')) { throw 'Requires Windows 11; Windows Server is outside this deployment target.' }
$nativeArch = (Get-ItemProperty 'HKLM:\SYSTEM\CurrentControlSet\Control\Session Manager\Environment').PROCESSOR_ARCHITECTURE
switch ($nativeArch) { 'AMD64' {$arch='amd64';$machine=0x8664} 'ARM64' {$arch='arm64';$machine=0xaa64} default {throw 'Unsupported native architecture.'} }
if($Stage -and !$PSBoundParameters.ContainsKey('Root')){throw '-Stage requires an explicit -Root.'}
$Root = [IO.Path]::GetFullPath($Root)
if ($Root.StartsWith('\\')) { throw 'Use a local NTFS user directory, not a network share.' }
$drive = New-Object IO.DriveInfo([IO.Path]::GetPathRoot($Root))
if ($drive.DriveFormat -ne 'NTFS') { throw 'The Agent state directory requires NTFS permissions.' }
$state = Join-Path $Root 'state'
$config = Join-Path $state 'agent.yaml'
$entry = Join-Path $Root 'bin\aiserveweave-agent.exe'
$runner = Join-Path $Root 'run-agent.ps1'
$taskName = "AIServeWeave Agent $sid"
$work = $null
$lockOwned = $false

function Protect-Path([string]$Path) {
    $item = Get-Item -LiteralPath $Path -Force
    if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'Reparse points are not permitted in the installation tree.' }
    if ($item.PSIsContainer) { $acl = New-Object Security.AccessControl.DirectorySecurity }
    else { $acl = New-Object Security.AccessControl.FileSecurity }
    $acl.SetOwner($identity.User)
    $acl.SetAccessRuleProtection($true,$false)
    foreach ($trusted in @($sid,'S-1-5-18','S-1-5-32-544')) {
        $trustee = New-Object Security.Principal.SecurityIdentifier($trusted)
        if ($item.PSIsContainer) {
            $rule = New-Object Security.AccessControl.FileSystemAccessRule($trustee,'FullControl','ContainerInherit,ObjectInherit','None','Allow')
        } else { $rule = New-Object Security.AccessControl.FileSystemAccessRule($trustee,'FullControl','Allow') }
        $acl.AddAccessRule($rule)
    }
    Set-Acl -LiteralPath $Path -AclObject $acl
}
function New-PrivateDirectory([string]$Path) {
    if (!(Test-Path -LiteralPath $Path)) { [void](New-Item -ItemType Directory -Path $Path) }
    Protect-Path $Path
}
function Invoke-Agent([string]$Binary,[string[]]$AgentArguments) {
    # Native invocation passes arguments as data; no cmd.exe or evaluated command string.
    # 原生调用把参数作为数据传入，不使用 cmd.exe 或求值命令字符串。
    $output = & $Binary @AgentArguments 2>&1
    if ($LASTEXITCODE -ne 0) { throw "Agent command failed (exit $LASTEXITCODE). Inspect the private configuration and logs." }
    return $output
}
function Get-HttpsFile([string]$Url,[string]$Destination) {
    $uri=[uri]$Url
    for($redirect=0;$redirect -lt 6;$redirect++) {
        if(!$uri.IsAbsoluteUri -or $uri.Scheme -ne 'https'){throw 'Download and redirects must use HTTPS.'}
        $request=[Net.HttpWebRequest]::Create($uri)
        $request.AllowAutoRedirect=$false;$request.Timeout=30000;$request.ReadWriteTimeout=60000
        $response=$request.GetResponse()
        try {
            $code=[int]$response.StatusCode
            if($code -ge 300 -and $code -lt 400){$uri=New-Object uri($uri,$response.Headers['Location']);continue}
            if($code -ne 200){throw 'Unexpected release download response.'}
            $inputStream=$response.GetResponseStream();$outputStream=[IO.File]::Create($Destination)
            try {
                $buffer=New-Object byte[] 65536;$total=0L;$elapsed=[Diagnostics.Stopwatch]::StartNew()
                while(($count=$inputStream.Read($buffer,0,$buffer.Length)) -gt 0){
                    $total+=$count
                    if($total -gt 1GB -or $elapsed.Elapsed.TotalSeconds -gt 600){throw 'Release download exceeded limits.'}
                    $outputStream.Write($buffer,0,$count)
                }
            } finally {$outputStream.Dispose();$inputStream.Dispose()}
            return
        } finally {$response.Dispose()}
    }
    throw 'Release download exceeded redirect limit.'
}
function Connect-Scheduler {
    $scheduler = New-Object -ComObject 'Schedule.Service'
    $scheduler.Connect()
    return $scheduler.GetFolder('\')
}
function Get-AgentTask {
    try { return (Connect-Scheduler).GetTask($taskName) }
    catch [Runtime.InteropServices.COMException] {
        if ($_.Exception.HResult -eq -2147024894) { return $null }
        throw
    }
}
function Stop-Agent {
    if ($Stage) { return }
    $task = Get-AgentTask
    if($null -ne $task){$task.Enabled=$false}
    # Capture the actual Agent PID before the control socket closes during draining.
    # 在排空期间控制连接关闭前记录实际 Agent PID。
    $agentProcess = $null
    if (Test-Path -LiteralPath $entry) {
        try {
            $status = (Invoke-Agent $entry @('-config',$config,'-control','status') | Out-String | ConvertFrom-Json)
            $agentProcess = Get-Process -Id $status.status.pid -ErrorAction Stop
            [void](Invoke-Agent $entry @('-config',$config,'-control','stop'))
        } catch {
            if ($null -ne $task -and $task.State -eq 4) { throw 'Running task did not acknowledge local stop; no binary was replaced.' }
        }
    }
    if ($null -ne $agentProcess -and !$agentProcess.WaitForExit(25000)) { throw 'Agent exceeded stop deadline; no binary was replaced.' }
    if ($null -ne $task -and $task.State -eq 4) {
        # Agent has exited; stopping the runner prevents a queued logon launch during maintenance.
        # Agent 已退出；停止包装任务，避免维护期间排队的登录触发。
        $task.Stop(0)
    }
}
function Start-Agent {
    if ($Stage) { return }
    $task = Get-AgentTask
    if ($null -eq $task) { throw 'Agent task is not installed.' }
    $task.Enabled=$true
    [void]$task.Run($null)
    for ($attempt=0;$attempt -lt 120;$attempt++) {
        try {
            $status=Invoke-Agent $entry @('-config',$config,'-control','status')
            $health=$status | Out-String | ConvertFrom-Json
            if($health.status.phase -eq 'running'){$status;return}
        } catch { }
        Start-Sleep -Seconds 1
    }
    throw 'Agent local control did not become ready. Inspect state\logs\agent.log; node connectivity requires separate verification.'
}
function Register-AgentTask {
    $folder = Connect-Scheduler
    $scheduler = New-Object -ComObject 'Schedule.Service';$scheduler.Connect()
    $definition = $scheduler.NewTask(0)
    $definition.RegistrationInfo.Description = 'AIServeWeave native Agent, current user logon only'
    $definition.Principal.UserId = $sid
    $definition.Principal.LogonType = 3
    $definition.Principal.RunLevel = 0
    $trigger = $definition.Triggers.Create(9);$trigger.UserId=$sid;$trigger.Enabled=$true
    $definition.Settings.Enabled=$true
    $definition.Settings.ExecutionTimeLimit='PT0S'
    $definition.Settings.DisallowStartIfOnBatteries=$false
    $definition.Settings.StopIfGoingOnBatteries=$false
    $definition.Settings.MultipleInstances=2
    $definition.Settings.RestartCount=3
    $definition.Settings.RestartInterval='PT1M'
    $definition.Settings.StartWhenAvailable=$true
    $exec = $definition.Actions.Create(0)
    $exec.Path = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
    $exec.Arguments = '-NoLogo -NoProfile -NonInteractive -WindowStyle Hidden -File "' + $runner + '"'
    $exec.WorkingDirectory=$Root
    [void]$folder.RegisterTaskDefinition($taskName,$definition,6,$sid,$null,3,$null)
}

$stageMarker=Join-Path $Root '.staging-only'
if($Stage -and (Test-Path -LiteralPath $Root) -and !(Test-Path -LiteralPath $stageMarker)){throw 'Staging refuses an existing non-staging tree.'}
if(!$Stage -and (Test-Path -LiteralPath $stageMarker)){throw 'A staged tree cannot be activated; use a fresh deployment root.'}
New-PrivateDirectory $Root
if($Stage){[IO.File]::WriteAllText($stageMarker,'staging only')}
$lock = Join-Path $Root '.maintenance-lock'
try {
    [void](New-Item -ItemType Directory -Path $lock -ErrorAction Stop);$lockOwned=$true
    $work=Join-Path $Root ('.prepare-'+[Guid]::NewGuid().ToString('N'));New-PrivateDirectory $work
    switch ($Action) {
        'status' { $task=Get-AgentTask;if($null -ne $task){Write-Output "Task state: $($task.State), last result: $($task.LastTaskResult)"};Invoke-Agent $entry @('-config',$config,'-control','status');return }
        'stop' { Stop-Agent;return }
        'start' { Start-Agent;return }
        'reload' { Invoke-Agent $entry @('-config',$config,'-control','reload');return }
        'uninstall' {
            Stop-Agent
            if (!$Stage -and $null -ne (Get-AgentTask)) { (Connect-Scheduler).DeleteTask($taskName,0) }
            if(Test-Path -LiteralPath $entry){Remove-Item -LiteralPath $entry}
            Write-Output "Service removed. State, identities, CLI logins and releases retained at $Root";return
        }
    }
    if ($Version -notmatch '^v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.-]+)?$') { throw 'Specify an exact -Version vX.Y.Z.' }
    $asset="aiserveweave-agent_${Version}_windows_${arch}.exe"
    $releaseDir=Join-Path $Root "releases\$Version"
    $candidate=Join-Path $work $asset
    if ($Action -eq 'rollback') {
        Copy-Item -LiteralPath (Join-Path $releaseDir $asset) -Destination $candidate
        $Checksums=Join-Path $releaseDir 'SHA256SUMS.txt'
    } elseif ($File) {
        if (!$Checksums) { throw 'Local installation requires -Checksums.' }
        Copy-Item -LiteralPath $File -Destination $candidate
    } else {
        if ($Checksums) { throw '-Checksums requires -File.' }
        if (!$BaseUrl -or !([uri]$BaseUrl).IsAbsoluteUri -or ([uri]$BaseUrl).Scheme -ne 'https') { throw 'Specify a trusted HTTPS exact release directory using -BaseUrl.' }
        [Net.ServicePointManager]::SecurityProtocol=[Net.SecurityProtocolType]::Tls12
        Get-HttpsFile "$BaseUrl/$asset" $candidate
        $Checksums=Join-Path $work 'SHA256SUMS.txt'
        Get-HttpsFile "$BaseUrl/SHA256SUMS.txt" $Checksums
    }
    $pattern='^([A-Fa-f0-9]{64})\s+\*?'+[regex]::Escape($asset)+'$'
    $digests=@(Get-Content -LiteralPath $Checksums | ForEach-Object {if($_ -match $pattern){$matches[1]}})
    if($digests.Count -ne 1 -or (Get-FileHash -LiteralPath $candidate -Algorithm SHA256).Hash -ne $digests[0]){throw 'Missing/ambiguous checksum or SHA256 mismatch.'}
    $stream=[IO.File]::OpenRead($candidate);$reader=New-Object IO.BinaryReader($stream)
    try {
        if($reader.ReadUInt16() -ne 0x5a4d){throw 'Expected native PE executable.'}
        $stream.Position=0x3c;$offset=$reader.ReadUInt32()
        if($offset -gt $stream.Length-6){throw 'Invalid PE header.'}
        $stream.Position=$offset
        if($reader.ReadUInt32() -ne 0x4550 -or $reader.ReadUInt16() -ne $machine){throw "Binary does not match native $arch architecture."}
    } finally {$reader.Dispose();$stream.Dispose()}
    if(!$Stage -and (Invoke-Agent $candidate @('-version') | Out-String).Trim() -ne "aiserveweave-agent $Version"){throw 'Embedded version mismatch.'}
    $retained=Join-Path $releaseDir $asset
    if((Test-Path -LiteralPath $retained) -and (Get-FileHash -LiteralPath $retained).Hash -ne $digests[0]){throw 'Release version already exists with different bytes.'}
    New-PrivateDirectory $state
    $identityDir=Join-Path $state 'identity';New-PrivateDirectory $identityDir
    if(!(Test-Path -LiteralPath $config)) {
        if($Action -ne 'install' -or !$NodeId -or !$Registry -or !$Gateway -or !(Test-Path -LiteralPath $CAFile) -or !(Test-Path -LiteralPath $TokenFile)){throw 'First install requires NodeId, Registry, Gateway, CAFile and TokenFile.'}
        $ca=Join-Path $identityDir 'ca.crt';$token=Join-Path $identityDir 'bootstrap.token'
        if((Test-Path -LiteralPath $ca) -or (Test-Path -LiteralPath $token)){throw 'Existing enrollment material requires explicit recovery.'}
        Copy-Item -LiteralPath $CAFile -Destination $ca;Protect-Path $ca
        Copy-Item -LiteralPath $TokenFile -Destination $token;Protect-Path $token
        if(!$Stage){[void](Invoke-Agent $candidate @('-config',$config,'-init-config','-node-id',$NodeId,'-registry',$Registry,'-gateway',$Gateway,'-ca-file',$ca,'-bootstrap-token-file',$token,'-cert-file',(Join-Path $identityDir 'node.crt'),'-key-file',(Join-Path $identityDir 'node.key')))}
    }
    if(!$Stage){[void](Invoke-Agent $candidate @('-config',$config,'-check-config'))}
    New-PrivateDirectory (Join-Path $Root 'releases');New-PrivateDirectory $releaseDir
    if(!(Test-Path -LiteralPath $retained)){Copy-Item -LiteralPath $candidate -Destination $retained}
    [IO.File]::WriteAllText((Join-Path $releaseDir 'SHA256SUMS.txt'),"$($digests[0])  $asset`n")
    if(Test-Path -LiteralPath $config){
        $backup=Join-Path $state 'backups';New-PrivateDirectory $backup
        Copy-Item -LiteralPath $config -Destination (Join-Path $backup ('agent.'+[DateTime]::UtcNow.ToString('yyyyMMddTHHmmssffff')+'.yaml'))
    }
    $environmentFile=Join-Path $Root 'environment.json'
    if(!(Test-Path -LiteralPath $environmentFile)){
        if(!$CliPath){$CliPath=$env:PATH}
        @{PATH=$CliPath;HOME=$env:USERPROFILE;USERPROFILE=$env:USERPROFILE;APPDATA=$env:APPDATA;LOCALAPPDATA=$env:LOCALAPPDATA;TEMP=$env:TEMP;TMP=$env:TMP} | ConvertTo-Json | Set-Content -LiteralPath $environmentFile -Encoding UTF8
        Protect-Path $environmentFile
    }
    $runnerContent=@'
# Run the Agent with the installed user environment, without a visible console.
# 使用已安装的用户环境运行 Agent，不显示控制台。
$ErrorActionPreference='Stop'
$root=$PSScriptRoot
$environment=Get-Content -LiteralPath (Join-Path $root 'environment.json') -Raw | ConvertFrom-Json
$environment.PSObject.Properties | ForEach-Object { [Environment]::SetEnvironmentVariable($_.Name,[string]$_.Value,'Process') }
function Quote-Argument([string]$Value) {
    return '"'+[regex]::Replace([regex]::Replace($Value,'(\\*)"','$1$1\"'),'(\\+)$','$1$1')+'"'
}
$start=New-Object Diagnostics.ProcessStartInfo
$start.FileName=Join-Path $root 'bin\aiserveweave-agent.exe'
$start.UseShellExecute=$false;$start.CreateNoWindow=$true
$start.WorkingDirectory=Join-Path $root 'state'
$arguments=@('-config',(Join-Path $root 'state\agent.yaml'),'-log-file',(Join-Path $root 'state\logs\agent.log'))
$start.Arguments=($arguments | ForEach-Object {Quote-Argument $_}) -join ' '
$process=[Diagnostics.Process]::Start($start)
$process.WaitForExit()
exit $process.ExitCode
'@
    Stop-Agent
    New-PrivateDirectory (Join-Path $Root 'bin');New-PrivateDirectory (Join-Path $state 'logs')
    # Replace only after the old executable has exited; retained releases are immutable.
    # 仅在旧程序退出后替换；保留的发布二进制不可变。
    $next=Join-Path $Root 'bin\next.exe';Copy-Item -LiteralPath $candidate -Destination $next
    if(Test-Path -LiteralPath $entry){[IO.File]::Replace($next,$entry,$null)}else{[IO.File]::Move($next,$entry)}
    [IO.File]::WriteAllText($runner,$runnerContent,(New-Object Text.UTF8Encoding($false)));Protect-Path $runner
    if(!$Stage){Register-AgentTask;Start-Agent}
    Write-Output "$Action complete: $Version windows/$arch. Node and real CLI acceptance remain separate."
} finally {
    if($work -and (Test-Path -LiteralPath $work)){Remove-Item -LiteralPath $work -Recurse -Force}
    if($lockOwned){Remove-Item -LiteralPath $lock -Force}
}
