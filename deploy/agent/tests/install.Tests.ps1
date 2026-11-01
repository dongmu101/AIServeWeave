# Black-box tests of install.ps1 against a local HTTPS stub of the platform, with agent.ps1
# replaced by a recorder; the real agent.ps1 is covered by deployment.Tests.ps1.
# install.ps1 对本地 HTTPS 平台桩的黑盒测试，agent.ps1 由记录器替代；真实 agent.ps1 由
# deployment.Tests.ps1 覆盖。
$ErrorActionPreference='Stop'
Set-StrictMode -Version Latest
$repo=Split-Path (Split-Path (Split-Path $PSScriptRoot))
$installText=[IO.File]::ReadAllText((Join-Path $repo 'deploy/agent/install.ps1'),[Text.Encoding]::UTF8)
$version='v0.99.1'
$code='ABCD-EFGH-JKMN-PQRS-TVWX'
$secretToken='secret-fixture-token'
$caPem="-----BEGIN CERTIFICATE-----`nZml4dHVyZQ==`n-----END CERTIFICATE-----`n"
$caSha=(New-Object Security.Cryptography.SHA256Managed).ComputeHash([Text.Encoding]::UTF8.GetBytes($caPem))|ForEach-Object -Begin{$h=''} -Process{$h+=$_.ToString('x2')} -End{$h}
$temp=Join-Path ([IO.Path]::GetTempPath()) ('aisw-install space-'+[guid]::NewGuid().ToString('N'))
[void](New-Item -ItemType Directory -Path $temp)
$python=@('python','python3')|ForEach-Object{Get-Command $_ -ErrorAction SilentlyContinue}|Select-Object -First 1
if(!$python){throw 'python is required for the HTTPS stub.'}
$openssl=Get-Command openssl -ErrorAction SilentlyContinue
if(!$openssl){$openssl=Get-Command (Join-Path $env:ProgramFiles 'Git\usr\bin\openssl.exe') -ErrorAction SilentlyContinue}
if(!$openssl){throw 'openssl is required for the HTTPS stub certificate.'}
$cert=Join-Path $temp 'cert.pem';$key=Join-Path $temp 'key.pem'
& $openssl.Source req -x509 -newkey rsa:2048 -nodes -days 2 -subj '/CN=127.0.0.1' -addext 'subjectAltName=IP:127.0.0.1' -keyout $key -out $cert 2>$null
if($LASTEXITCODE -ne 0){throw 'Certificate generation failed.'}
# The stub's certificate is self-signed; trust it for this process only.
# 桩的证书是自签的；仅在本进程内信任它。
# A compiled callback is needed: a script block cannot run on the TLS handshake thread.
# 需要编译出的回调：脚本块无法在 TLS 握手线程上运行。
Add-Type -TypeDefinition @"
using System.Net.Security;
using System.Security.Cryptography.X509Certificates;
public static class AiswTrustStub {
    public static bool Accept(object sender, X509Certificate certificate, X509Chain chain, SslPolicyErrors errors) { return true; }
    public static RemoteCertificateValidationCallback Callback { get { return Accept; } }
}
"@
[Net.ServicePointManager]::ServerCertificateValidationCallback=[AiswTrustStub]::Callback

function Assert-True($Condition,[string]$Message){if(!$Condition){throw "FAILED: $Message"}}
function Assert-Throws([scriptblock]$Block,[string]$Pattern,[string]$Message){
    $thrown=$null
    try{& $Block}catch{$thrown=$_.Exception.Message}
    Assert-True ($null -ne $thrown) "$Message (nothing was thrown)"
    Assert-True ($thrown -match $Pattern) "$Message (got: $thrown)"
}

function New-Fixture([string]$Name,[hashtable]$Mutate=@{}){
    # A directory of artifacts as the platform would serve them / 平台会提供的制品目录
    $dir=Join-Path $temp $Name;[void](New-Item -ItemType Directory -Path $dir)
    $asset="aiserveweave-agent_${version}_windows_amd64.exe"
    [IO.File]::WriteAllBytes((Join-Path $dir $asset),[Text.Encoding]::ASCII.GetBytes('fake-agent-binary'))
    [IO.File]::WriteAllText((Join-Path $dir 'agent.ps1'),@'
param([string]$Action,[string]$Version,[string]$File,[string]$Checksums,[string]$NodeId,[string]$Registry,[string]$Gateway,[string]$CAFile,[string]$TokenFile,[string]$Root,[string]$CliPath,[switch]$Stage)
$record=@{Action=$Action;Version=$Version;NodeId=$NodeId;Registry=$Registry;Gateway=$Gateway;Stage=[bool]$Stage;CliPath=$CliPath
  FileSha=(Get-FileHash -LiteralPath $File).Hash;HasSums=(Test-Path -LiteralPath $Checksums)}
if($TokenFile){$record.Token=([IO.File]::ReadAllText($TokenFile)).Trim();$record.CA=[IO.File]::ReadAllText($CAFile)}
[void](New-Item -ItemType Directory -Force -Path $Root)
$record|ConvertTo-Json|Set-Content -LiteralPath (Join-Path $Root 'call.json')
'@)
    foreach($name in $Mutate.Keys){if($null -eq $Mutate[$name]){Remove-Item -LiteralPath (Join-Path $dir $name)}else{[IO.File]::WriteAllText((Join-Path $dir $name),$Mutate[$name])}}
    $lines=Get-ChildItem -LiteralPath $dir -File|ForEach-Object{(Get-FileHash -LiteralPath $_.FullName).Hash.ToLower()+'  '+$_.Name}
    [IO.File]::WriteAllText((Join-Path $dir 'SHA256SUMS.txt'),(($lines -join "`n")+"`n"))
    return $dir
}

function Start-Stub([string]$Dir,[string]$StubVersion='',[int]$RedeemStatus=200){
    $log=Join-Path $Dir 'redeems.log'
    $info=New-Object Diagnostics.ProcessStartInfo
    $info.FileName=$python.Source
    $info.Arguments=('"{0}" "{1}" "{2}" "{3}" windows amd64 "{4}" "{5}" {6}' -f (Join-Path $PSScriptRoot 'platform_stub.py'),$cert,$key,$Dir,$log,$StubVersion,$RedeemStatus)
    $info.UseShellExecute=$false;$info.RedirectStandardInput=$true;$info.RedirectStandardOutput=$true
    $process=[Diagnostics.Process]::Start($info)
    $url=$process.StandardOutput.ReadLine()
    return @{Process=$process;Url=$url;Log=$log}
}
function Stop-Stub($Stub){
    $Stub.Process.StandardInput.Close()
    [void]$Stub.Process.WaitForExit(15000)
    if(Test-Path -LiteralPath $Stub.Log){return ,@(([IO.File]::ReadAllText($Stub.Log)) -split "`n"|Where-Object{$_})}
    return ,@()
}
function Invoke-Install([hashtable]$Stub,[string]$Root,[hashtable]$Arguments){
    # Named parameters are written out: splatting a hashtable into a script block that
    # has no param block would turn a switch's value into a positional argument.
    # 参数逐个写明：把哈希表展开到没有 param 块的脚本块，会把开关的值变成位置参数。
    function Get-Argument([string]$Name,$Default=''){if($Arguments.ContainsKey($Name)){return $Arguments[$Name]}else{return $Default}}
    $block=[scriptblock]::Create($installText)
    # -Upgrade:$false would not survive the script's own $args forwarding either, so the switch is only ever passed bare.
    # -Upgrade:$false 也经不起脚本自身的 $args 转发，所以开关只以裸形式传入。
    if(Get-Argument 'Upgrade' $false){
        & $block -Platform $Stub.Url -Root $Root -Stage -Upgrade *>&1|Out-String
    } else {
        & $block -Platform $Stub.Url -Root $Root -Stage `
            -Code (Get-Argument 'Code') -Token (Get-Argument 'Token') -NodeId (Get-Argument 'NodeId') -CaSha256 (Get-Argument 'CaSha256') `
            -Registry (Get-Argument 'Registry') -Gateway (Get-Argument 'Gateway') -Bundle (Get-Argument 'Bundle') `
            -Source (Get-Argument 'Source' 'platform') *>&1|Out-String
    }
}
function Read-Call([string]$Root){Get-Content -LiteralPath (Join-Path $Root 'call.json') -Raw|ConvertFrom-Json}

try{
    # 1. A code install redeems once, after the downloads, and hands over verified material.
    # 1. 兑换码安装在下载之后只兑换一次，并交出已校验的材料。
    $dir=New-Fixture 'code';$stub=Start-Stub $dir;$root=Join-Path $temp 'root code'
    $savedPreference=$ErrorActionPreference
    $output=Invoke-Install $stub $root @{Code=$code}
    $redeems=Stop-Stub $stub
    $call=Read-Call $root
    Assert-True ($call.Action -eq 'install' -and $call.Version -eq $version -and $call.NodeId -eq 'node-redeemed') 'handoff arguments'
    Assert-True ($call.Token -eq $secretToken -and $call.CA -eq $caPem -and $call.Stage -eq $true -and $call.HasSums) 'credential files reached agent.ps1'
    Assert-True ($redeems.Count -eq 1 -and $redeems[0] -like "*$code*") 'the code was redeemed exactly once'
    Assert-True (!$output.Contains($code) -and !$output.Contains($secretToken)) 'output must not contain the code or token'
    # The anonymous block must not leak into the caller / 匿名块不得泄漏到调用方
    Assert-True ($ErrorActionPreference -eq $savedPreference) 'ErrorActionPreference leaked'
    Assert-True ($null -eq (Get-Variable -Name CaSha256 -ErrorAction SilentlyContinue)) 'a parameter variable leaked'
    Assert-True ($null -eq (Get-Command Get-HttpsFile -ErrorAction SilentlyContinue)) 'a helper function leaked'
    Assert-True (!(Get-ChildItem ([IO.Path]::GetTempPath()) -Directory -Filter 'aisw-install-*' -ErrorAction SilentlyContinue)) 'the temporary directory was not removed'

    # 2. A refused code installs nothing / 被拒绝的兑换码不会安装任何东西
    foreach($case in @(@{Status=404;Pattern='invalid, already used or expired'},@{Status=429;Pattern='Too many attempts'},@{Status=503;Pattern='could not redeem'})){
        $dir=New-Fixture "refused$($case.Status)";$stub=Start-Stub $dir '' $case.Status;$root=Join-Path $temp "root$($case.Status)"
        Assert-Throws {Invoke-Install $stub $root @{Code=$code}} $case.Pattern "status $($case.Status)"
        [void](Stop-Stub $stub)
        Assert-True (!(Test-Path -LiteralPath (Join-Path $root 'call.json'))) 'a refused code must not install'
    }

    # 3. A failed download or a bad checksum never spends the code / 下载失败或校验和不符绝不消耗兑换码
    foreach($case in @(@{Name='missing';Mutate=@{'agent.ps1'=$null};Pattern='HTTP 404|exactly once'},@{Name='tampered';Mutate=@{};Tamper=$true;Pattern='SHA256 mismatch'})){
        $dir=New-Fixture "bad$($case.Name)" $case.Mutate
        if($case.ContainsKey('Tamper')){Add-Content -LiteralPath (Join-Path $dir 'agent.ps1') -Value '# tampered' -Encoding ASCII}
        $stub=Start-Stub $dir;$root=Join-Path $temp "rootbad$($case.Name)"
        Assert-Throws {Invoke-Install $stub $root @{Code=$code}} $case.Pattern "bad download $($case.Name)"
        Assert-True ((Stop-Stub $stub).Count -eq 0) "the code was spent after $($case.Name)"
        Assert-True (!(Test-Path -LiteralPath (Join-Path $root 'call.json'))) 'a bad download must not install'
    }

    # 4. Token credential pins the CA fingerprint / 令牌直写按指纹固定 CA
    $dir=New-Fixture 'token';$stub=Start-Stub $dir;$root=Join-Path $temp 'root token'
    $tokenArguments=@{NodeId='node-token';Token=$secretToken;Registry='registry.test:9443';Gateway='gateway.test:8443'}
    # Run it from a non-strict scope: strict mode set inside the script must not outlive it.
    # 从非严格作用域运行：脚本内设置的严格模式不得在它结束后依然生效。
    $strictLeaked=& {
        Set-StrictMode -Off
        $script:output=Invoke-Install $stub $root ($tokenArguments+@{CaSha256=$caSha.ToUpper()})
        try{[void]$undefinedVariableForStrictModeCheck;$false}catch{$true}
    }
    $output=$script:output
    Assert-True (!$strictLeaked) 'strict mode leaked'
    Assert-True ((Read-Call $root).Token -eq $secretToken) 'token credential reached agent.ps1'
    Assert-True (!$output.Contains($secretToken)) 'token must not be printed'
    $root2=Join-Path $temp 'root token wrong'
    Assert-Throws {Invoke-Install $stub $root2 ($tokenArguments+@{CaSha256=('0'*64)})} 'does not match the expected SHA256' 'wrong CA fingerprint'
    Assert-True (!(Test-Path -LiteralPath (Join-Path $root2 'call.json'))) 'a wrong fingerprint must not install'
    Assert-True ((Stop-Stub $stub).Count -eq 0) 'token installs never redeem'

    # 5. An enrollment ZIP / 接入 ZIP
    $dir=New-Fixture 'bundle';$stub=Start-Stub $dir;$root=Join-Path $temp 'root bundle'
    $bundleDir=Join-Path $temp 'bundle files';[void](New-Item -ItemType Directory -Path $bundleDir)
    [IO.File]::WriteAllText((Join-Path $bundleDir 'ca.pem'),$caPem);[IO.File]::WriteAllText((Join-Path $bundleDir 'bootstrap-token'),"$secretToken`n")
    [IO.File]::WriteAllText((Join-Path $bundleDir 'connection.json'),(@{node_id='node-bundle';registry_addr='registry.test:9443';gateway_addr='gateway.test:8443';ca_sha256=$caSha}|ConvertTo-Json))
    $zip=Join-Path $temp 'bundle.zip'
    Compress-Archive -Path (Join-Path $bundleDir '*') -DestinationPath $zip
    [void](Invoke-Install $stub $root @{Bundle=$zip})
    Assert-True ((Read-Call $root).NodeId -eq 'node-bundle') 'bundle credential reached agent.ps1'
    [void](Stop-Stub $stub)

    # 6. Rules that need no credential / 不涉及凭据的规则
    $dir=New-Fixture 'rules';$stub=Start-Stub $dir;$root=Join-Path $temp 'root rules'
    Assert-Throws {Invoke-Install $stub $root @{Code=$code;Bundle=$zip}} 'Use only one of' 'exclusive credentials'
    Assert-Throws {Invoke-Install $stub $root @{Upgrade=$true}} 'No installed Agent' 'upgrade needs an install'
    Assert-Throws {Invoke-Install $stub $root @{Code=$code;Source='http://example.invalid/x'}} '-Source must be' 'plain HTTP source'
    Assert-Throws {Invoke-Install $stub $root @{Code=$code;Source='github'}} 'does not name a GitHub repository' 'github needs a repository'
    $plain=[scriptblock]::Create($installText)
    Assert-Throws {& $plain -Platform 'http://127.0.0.1:1' -Root $root -Stage -Code $code} "platform's https origin" 'plain HTTP platform'
    Assert-True ((Stop-Stub $stub).Count -eq 0) 'rule violations must not redeem'
    [void](New-Item -ItemType Directory -Path (Join-Path $root 'state') -Force);[IO.File]::WriteAllText((Join-Path $root 'state/agent.yaml'),'existing configuration')
    $stub=Start-Stub (New-Fixture 'installed')
    Assert-Throws {Invoke-Install $stub $root @{Code=$code}} 'use -Upgrade' 'installed nodes need -Upgrade'
    [void](Invoke-Install $stub $root @{Upgrade=$true})
    Assert-True ((Read-Call $root).Action -eq 'upgrade') 'upgrade handoff'
    Assert-True ((Stop-Stub $stub).Count -eq 0) 'upgrade must not redeem'

    # 7. A dirty platform build is refused before anything else / dirty 平台构建在其他一切之前被拒绝
    $stub=Start-Stub (New-Fixture 'dirty') "$version-3-gabc1234-dirty";$root=Join-Path $temp 'root dirty'
    Assert-Throws {Invoke-Install $stub $root @{Code=$code}} 'dirty tree' 'dirty version'
    Assert-True ((Stop-Stub $stub).Count -eq 0) 'a dirty version must not redeem'

    # 8. The script served by the platform carries its own origin / 平台下发的脚本自带站点地址
    $dir=New-Fixture 'served';$stub=Start-Stub $dir;$root=Join-Path $temp 'root served'
    $served=[scriptblock]::Create($installText.Replace('__AISW_PLATFORM_URL__',$stub.Url))
    & $served -Root $root -Stage -Token $secretToken -NodeId node-served -CaSha256 $caSha -Registry registry.test:9443 -Gateway gateway.test:8443 *>&1|Out-Null
    Assert-True ((Read-Call $root).NodeId -eq 'node-served') 'a served script needs no -Platform'
    [void](Stop-Stub $stub)
    Write-Output 'install.ps1 tests passed.'
} finally {
    [Net.ServicePointManager]::ServerCertificateValidationCallback=$null
    Remove-Item -LiteralPath $temp -Recurse -Force -ErrorAction SilentlyContinue
}
