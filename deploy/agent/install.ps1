# One-line bootstrap for the native Agent on Windows 11 (current ordinary user).
# It downloads the Agent and agent.ps1 from the platform (or GitHub Releases),
# verifies them, redeems the credential, then hands over to agent.ps1.
#
#   & ([scriptblock]::Create((irm https://PLATFORM/agent/install.ps1))) -Code CODE
#   irm https://PLATFORM/agent/install.ps1 | iex      # prompts for the install code
#
# 原生 Agent 在 Windows 11（当前普通用户）上的一行命令引导脚本。它从平台（或 GitHub Releases）
# 下载 Agent 与 agent.ps1 并校验，兑换凭据，然后交给 agent.ps1 完成安装。
#
# The whole script is one anonymous block: it works when dot-sourced by iex without
# leaking variables, functions, strict mode or $ErrorActionPreference into the
# caller's session, and without a script file (so ExecutionPolicy does not apply).
# 整个脚本是一个匿名块：被 iex 执行时不会把变量、函数、严格模式或 $ErrorActionPreference
# 泄漏进调用方会话，也不需要脚本文件（因此不受 ExecutionPolicy 限制）。
& {
    param(
        [string]$Code, [string]$NodeId, [string]$Token, [string]$CaSha256,
        [string]$Registry, [string]$Gateway, [string]$Bundle,
        [string]$Source = 'platform', [string]$Version, [string]$Platform,
        [string]$CliPath, [string]$Root, [switch]$Upgrade, [switch]$Stage
    )
    Set-StrictMode -Version Latest
    $ErrorActionPreference = 'Stop'

    # The platform serves this file with the placeholder replaced by its own origin.
    # The comparison below uses only a prefix, or serving would rewrite it too.
    # 平台下发本文件时，占位符会被替换为平台自己的站点地址。下面的判断只用前缀，否则下发时
    # 它也会被一并替换。
    $servedPlatform = '__AISW_PLATFORM_URL__'

    $settingsUrl = 'http://127.0.0.1:8899'
    $work = $null
    $previousProtocol = [Net.ServicePointManager]::SecurityProtocol

    function Get-Field($Object, [string]$Name) {
        $property = $Object.PSObject.Properties[$Name]
        if ($null -eq $property -or $null -eq $property.Value) { return '' }
        return [string]$property.Value
    }

    # Get-HttpsFile downloads an https URL to a file. Redirects are followed by hand
    # so a redirect to plain HTTP is refused; the size and time are bounded.
    # Get-HttpsFile 把 https URL 下载到文件。重定向手动跟随，因此拒绝跳转到明文 HTTP；大小与耗时有界。
    function Get-HttpsFile([string]$Url, [string]$Destination, [int]$TimeoutSeconds = 600) {
        $uri = [uri]$Url
        for ($redirect = 0; $redirect -lt 6; $redirect++) {
            if (!$uri.IsAbsoluteUri -or $uri.Scheme -ne 'https') { throw 'Downloads and redirects must use HTTPS.' }
            $request = [Net.HttpWebRequest]::Create($uri)
            $request.AllowAutoRedirect = $false
            $request.Timeout = 30000
            $request.ReadWriteTimeout = 60000
            try { $response = $request.GetResponse() }
            catch [Net.WebException] {
                if ($null -ne $_.Exception.Response) { throw "Download failed (HTTP $([int]$_.Exception.Response.StatusCode)): $($uri.AbsolutePath)" }
                throw "Cannot reach $($uri.Host): $($_.Exception.Message)"
            }
            try {
                $status = [int]$response.StatusCode
                if ($status -ge 300 -and $status -lt 400) { $uri = New-Object uri($uri, $response.Headers['Location']); continue }
                if ($status -ne 200) { throw "Download failed (HTTP $status): $($uri.AbsolutePath)" }
                $inputStream = $response.GetResponseStream()
                $outputStream = [IO.File]::Create($Destination)
                try {
                    $buffer = New-Object byte[] 65536
                    $total = 0L
                    $clock = [Diagnostics.Stopwatch]::StartNew()
                    while (($count = $inputStream.Read($buffer, 0, $buffer.Length)) -gt 0) {
                        $total += $count
                        if ($total -gt 1GB -or $clock.Elapsed.TotalSeconds -gt $TimeoutSeconds) { throw 'Download exceeded its size or time limit.' }
                        $outputStream.Write($buffer, 0, $count)
                    }
                } finally { $outputStream.Dispose(); $inputStream.Dispose() }
                return
            } finally { $response.Dispose() }
        }
        throw 'Download exceeded the redirect limit.'
    }

    function Test-Sha256([string]$Path, [string]$Expected) {
        return ((Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash -ieq $Expected)
    }

    # Read-InstallCode prompts for the install code without echo.
    # Read-InstallCode 无回显地提示输入安装码。
    function Read-InstallCode {
        try { $secure = Read-Host -Prompt 'Install code (input hidden)' -AsSecureString }
        catch { throw 'No terminal to read the install code from; pass -Code, -Token or -Bundle.' }
        $pointer = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure)
        try { return [Runtime.InteropServices.Marshal]::PtrToStringBSTR($pointer) }
        finally { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($pointer) }
    }

    # Invoke-Redeem trades the install code for the enrollment material. The code
    # only goes into the request body; every refusal reads alike on purpose.
    # Invoke-Redeem 用安装码换取接入材料。安装码只进请求体；各种拒绝情形刻意读起来一样。
    function Invoke-Redeem([string]$Url, [string]$Secret) {
        $body = [Text.Encoding]::UTF8.GetBytes('{"code":"' + $Secret + '"}')
        $request = [Net.HttpWebRequest]::Create($Url)
        $request.Method = 'POST'
        $request.ContentType = 'application/json'
        $request.AllowAutoRedirect = $false
        $request.Timeout = 30000
        $request.ContentLength = $body.Length
        try {
            $stream = $request.GetRequestStream()
            try { $stream.Write($body, 0, $body.Length) } finally { $stream.Dispose() }
            $response = $request.GetResponse()
        } catch [Net.WebException] {
            $response = $_.Exception.Response
            if ($null -eq $response) { throw 'Cannot reach the platform to redeem the install code.' }
        }
        try {
            $status = [int]$response.StatusCode
            $text = ''
            if ($status -eq 200) {
                $reader = New-Object IO.StreamReader($response.GetResponseStream(), [Text.Encoding]::UTF8)
                try { $text = $reader.ReadToEnd() } finally { $reader.Dispose() }
            }
            return @{ Status = $status; Body = $text }
        } finally { $response.Dispose() }
    }

    function Write-PrivateText([string]$Path, [string]$Text) {
        [IO.File]::WriteAllText($Path, $Text, (New-Object Text.UTF8Encoding($false)))
    }

    try {
        # ---- environment ----
        $onWindows = [Environment]::OSVersion.Platform -eq 'Win32NT'
        if (!$Stage) {
            if (!$onWindows) { throw 'install.ps1 is for Windows; use install.sh on Ubuntu and macOS.' }
            $principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
            if ($principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
                throw 'Run this in an ordinary (non-elevated) PowerShell window as the intended user.'
            }
        }
        switch ([Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()) {
            'X64' { $arch = 'amd64' }
            'Arm64' { $arch = 'arm64' }
            default { throw 'Unsupported CPU architecture.' }
        }
        if ($Stage -and !$Root) { throw '-Stage requires -Root.' }
        if (!$Root) { $Root = Join-Path $env:LOCALAPPDATA 'AIServeWeave\Agent' }
        $config = Join-Path (Join-Path $Root 'state') 'agent.yaml'

        # ---- inputs ----
        $modes = 0
        if ($Code) { $modes++ }
        if ($Token -or $NodeId -or $CaSha256) { $modes++ }
        if ($Bundle) { $modes++ }
        if ($modes -gt 1) { throw 'Use only one of -Code, -Token (with -NodeId/-CaSha256/-Registry/-Gateway), -Bundle.' }
        if ($Upgrade) {
            if ($modes -ne 0) { throw '-Upgrade does not take a credential.' }
            if (!(Test-Path -LiteralPath $config)) { throw 'No installed Agent found for -Upgrade.' }
        } elseif (Test-Path -LiteralPath $config) {
            throw 'An Agent is already installed here; use -Upgrade to update it (a new credential would not replace its identity).'
        }
        if ($Token -or $NodeId -or $CaSha256 -or $Registry -or $Gateway) {
            if (!$Token -or !$NodeId -or !$CaSha256 -or !$Registry -or !$Gateway) { throw 'Token install needs -NodeId, -Token, -CaSha256, -Registry and -Gateway together.' }
            if ($NodeId -notmatch '^[A-Za-z0-9._-]+$') { throw 'Invalid -NodeId.' }
            if ($CaSha256 -notmatch '^[A-Fa-f0-9]{64}$') { throw '-CaSha256 must be 64 hex characters.' }
            if ($Registry -notmatch '^[A-Za-z0-9.:-]+$') { throw 'Invalid -Registry.' }
            if ($Gateway -notmatch '^[A-Za-z0-9.:,-]+$') { throw 'Invalid -Gateway.' }
        }
        if ($Code -and $Code -notmatch '^[A-Za-z0-9-]{16,40}$') { throw 'The install code is malformed.' }
        if ($Bundle -and !(Test-Path -LiteralPath $Bundle -PathType Leaf)) { throw 'Enrollment ZIP not found.' }
        if (!$Platform -and !$servedPlatform.StartsWith('__AISW_')) { $Platform = $servedPlatform }
        $Platform = if ($Platform) { $Platform.TrimEnd('/') } else { '' }
        if ($Platform -notmatch '^https://[^/?#\s]+$') { throw "-Platform must be the platform's https origin, e.g. https://open7x.com" }
        if ($Source -notin @('platform', 'github') -and $Source -notmatch '^https://') { throw '-Source must be platform, github or an https:// release directory.' }
        $Source = $Source.TrimEnd('/')

        [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
        $work = Join-Path ([IO.Path]::GetTempPath()) ('aisw-install-' + [Guid]::NewGuid().ToString('N'))
        [void](New-Item -ItemType Directory -Path $work)

        # ---- manifest, version, source ----
        $manifestPath = Join-Path $work 'manifest.json'
        Get-HttpsFile "$Platform/agent/v1/manifest" $manifestPath 60
        $manifest = [IO.File]::ReadAllText($manifestPath, [Text.Encoding]::UTF8) | ConvertFrom-Json
        $platformVersion = Get-Field $manifest 'version'
        if (!$platformVersion) { throw 'The platform manifest has no version.' }
        if (!$Version) { $Version = $platformVersion }
        if ($Version -like '*-dirty') { throw "Version $Version was built from a dirty tree; agent.ps1 does not install it. Rebuild the platform from a clean commit or pick a release with -Version." }
        if ($Version -ceq 'dev') { throw 'This platform runs a development build; pick a release with -Version and -Source github.' }
        if ($Version -notmatch '^v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.-]+)?$' -and $Version -cnotmatch '^[0-9a-f]{7,40}$') { throw "Unsupported version $Version." }
        $asset = "aiserveweave-agent_${Version}_windows_${arch}.exe"
        switch -Regex ($Source) {
            '^platform$' {
                if ($Version -ne $platformVersion) { throw "The platform only serves $platformVersion; use -Source github for $Version." }
                $listed = @($manifest.assets | Where-Object { (Get-Field $_ 'name') -ceq $asset })
                if ($listed.Count -eq 0) { throw "The platform has no windows/$arch build of $Version." }
                $baseUrl = "$Platform/agent/download/$Version"
            }
            '^github$' {
                $github = $manifest.PSObject.Properties['github']
                $repo = if ($null -ne $github -and $null -ne $github.Value) { Get-Field $github.Value 'repo' } else { '' }
                if (!$repo) { throw 'This platform does not name a GitHub repository.' }
                if ($Version -notmatch '^v[0-9]+\.[0-9]+\.[0-9]+$') { throw 'GitHub Releases only carry exact vX.Y.Z versions.' }
                $baseUrl = "https://github.com/$repo/releases/download/$Version"
            }
            default { $baseUrl = $Source }
        }

        # ---- download and verify everything before redeeming anything ----
        Write-Output "==> downloading Agent $Version (windows/$arch)"
        $sumsPath = Join-Path $work 'SHA256SUMS.txt'
        $binaryPath = Join-Path $work $asset
        $agentScript = Join-Path $work 'agent.ps1'
        Get-HttpsFile "$baseUrl/SHA256SUMS.txt" $sumsPath 60
        Get-HttpsFile "$baseUrl/$asset" $binaryPath
        Get-HttpsFile "$baseUrl/agent.ps1" $agentScript 60
        foreach ($name in @($asset, 'agent.ps1')) {
            $pattern = '^([A-Fa-f0-9]{64})\s+\*?' + [regex]::Escape($name) + '$'
            $digests = @(Get-Content -LiteralPath $sumsPath | ForEach-Object { if ($_ -match $pattern) { $matches[1] } })
            if ($digests.Count -ne 1) { throw "SHA256SUMS.txt must list $name exactly once." }
            if (!(Test-Sha256 (Join-Path $work $name) $digests[0])) { throw "SHA256 mismatch for $name." }
        }

        # ---- credential ----
        $caPath = Join-Path $work 'ca.pem'
        $tokenPath = Join-Path $work 'bootstrap-token'
        if ($Upgrade) {
            $installArguments = @{}
        } else {
            if ($Bundle) {
                Add-Type -AssemblyName System.IO.Compression.FileSystem
                $archive = [IO.Compression.ZipFile]::OpenRead((Resolve-Path -LiteralPath $Bundle).Path)
                try {
                    $contents = @{}
                    foreach ($entryName in @('ca.pem', 'bootstrap-token', 'connection.json')) {
                        $entry = $archive.GetEntry($entryName)
                        if ($null -eq $entry) { throw "The ZIP has no $entryName." }
                        $reader = New-Object IO.StreamReader($entry.Open(), [Text.Encoding]::UTF8)
                        try { $contents[$entryName] = $reader.ReadToEnd() } finally { $reader.Dispose() }
                    }
                } finally { $archive.Dispose() }
                $connection = $contents['connection.json'] | ConvertFrom-Json
                $NodeId = Get-Field $connection 'node_id'
                $Registry = Get-Field $connection 'registry_addr'
                $Gateway = Get-Field $connection 'gateway_addr'
                $CaSha256 = Get-Field $connection 'ca_sha256'
                if (!$NodeId -or !$Registry -or !$Gateway -or !$CaSha256) { throw 'connection.json is incomplete.' }
                Write-PrivateText $caPath $contents['ca.pem']
                Write-PrivateText $tokenPath $contents['bootstrap-token']
            } elseif ($Token) {
                $caJson = Join-Path $work 'ca.json'
                Get-HttpsFile "$Platform/agent/v1/ca" $caJson 60
                $caPem = Get-Field ([IO.File]::ReadAllText($caJson, [Text.Encoding]::UTF8) | ConvertFrom-Json) 'ca_pem'
                if (!$caPem) { throw "The platform's CA reply was empty." }
                Write-PrivateText $caPath $caPem
                Write-PrivateText $tokenPath ($Token + "`n")
                $Token = $null
            } else {
                if (!$Code) { $Code = (Read-InstallCode) -replace '\s', '' }
                if ($Code -notmatch '^[A-Za-z0-9-]{16,40}$') { throw 'The install code is malformed.' }
                Write-Output '==> redeeming the install code'
                $reply = Invoke-Redeem "$Platform/agent/v1/redeem" $Code
                $Code = $null
                switch ($reply.Status) {
                    200 { }
                    404 { throw 'The install code is invalid, already used or expired; ask for a new one.' }
                    429 { throw 'Too many attempts; wait a minute and retry.' }
                    default { throw "The platform could not redeem the install code (HTTP $($reply.Status)); retry later." }
                }
                $redeemed = $reply.Body | ConvertFrom-Json
                $NodeId = Get-Field $redeemed 'node_id'
                $Registry = Get-Field $redeemed 'registry_addr'
                $Gateway = Get-Field $redeemed 'gateway_addr'
                $CaSha256 = Get-Field $redeemed 'ca_sha256'
                $caPem = Get-Field $redeemed 'ca_pem'
                $bootstrap = Get-Field $redeemed 'bootstrap_token'
                if (!$NodeId -or !$Registry -or !$Gateway -or !$CaSha256 -or !$caPem -or !$bootstrap) { throw "The platform's reply was incomplete." }
                Write-PrivateText $caPath $caPem
                Write-PrivateText $tokenPath ($bootstrap + "`n")
                $bootstrap = $null
                $reply = $null
                $redeemed = $null
            }
            if (!(Test-Sha256 $caPath $CaSha256)) { throw 'The CA does not match the expected SHA256; refusing to trust it.' }
            $installArguments = @{ NodeId = $NodeId; Registry = $Registry; Gateway = $Gateway; CAFile = $caPath; TokenFile = $tokenPath }
        }

        # ---- hand over to agent.ps1 ----
        # It runs as a script block read from the verified file, so ExecutionPolicy
        # does not stand in the way of a downloaded .ps1.
        # 它以读自已校验文件的脚本块运行，因此下载来的 .ps1 不会被 ExecutionPolicy 拦住。
        $agentBlock = [scriptblock]::Create([IO.File]::ReadAllText($agentScript, [Text.Encoding]::UTF8))
        $arguments = @{ Version = $Version; File = $binaryPath; Checksums = $sumsPath }
        foreach ($key in $installArguments.Keys) { $arguments[$key] = $installArguments[$key] }
        if ($CliPath) { $arguments['CliPath'] = $CliPath }
        if ($PSBoundParameters.ContainsKey('Root')) { $arguments['Root'] = $Root }
        if ($Stage) { $arguments['Stage'] = $true }
        $action = if ($Upgrade) { 'upgrade' } else { 'install' }
        & $agentBlock $action @arguments
        if ($Upgrade) {
            Write-Output "==> upgraded to $Version"
        } else {
            Write-Output "==> installed Agent $Version as node $NodeId"
            if (!$Stage) {
                $statusArguments = @{}
                if ($PSBoundParameters.ContainsKey('Root')) { $statusArguments['Root'] = $Root }
                try { & $agentBlock 'status' @statusArguments } catch { Write-Output 'Agent status is not available yet; check again shortly.' }
                Write-Output "Configure inference backends at $settingsUrl on this machine."
                Write-Output 'To upgrade later, run this installer again with -Upgrade.'
            }
        }
    } finally {
        [Net.ServicePointManager]::SecurityProtocol = $previousProtocol
        if ($work -and (Test-Path -LiteralPath $work)) { Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue }
    }
} @args
