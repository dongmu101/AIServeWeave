# Black-box staging tests; no tasks are registered and no user login is read.
# 黑盒暂存测试；不注册任务，不读取用户登录。
$ErrorActionPreference='Stop'
Set-StrictMode -Version Latest
$repo=Split-Path (Split-Path (Split-Path $PSScriptRoot))
$script=Join-Path $repo 'deploy\agent\agent.ps1'
$temp=Join-Path ([IO.Path]::GetTempPath()) ('aisw-deploy space-'+[guid]::NewGuid().ToString('N'))
[void](New-Item -ItemType Directory -Path $temp)
try {
    $arch=if((Get-ItemProperty 'HKLM:\SYSTEM\CurrentControlSet\Control\Session Manager\Environment').PROCESSOR_ARCHITECTURE -eq 'ARM64'){'arm64'}else{'amd64'}
    $asset="aiserveweave-agent_v0.99.1_windows_$arch.exe"
    $binary=Join-Path $temp $asset
    & go build '-ldflags=-X main.version=v0.99.1' -o $binary ./service/aiServeWeaveAgent
    if($LASTEXITCODE -ne 0){throw 'Fixture build failed.'}
    $manifest=Join-Path $temp 'SHA256SUMS.txt'
    $digest=(Get-FileHash -LiteralPath $binary).Hash
    [IO.File]::WriteAllText($manifest,"$digest  $asset`n"+('0'*64)+"  unrelated_asset`n")
    $ca=Join-Path $temp 'ca.crt';$token=Join-Path $temp 'token'
    [IO.File]::WriteAllText($ca,'fixture-ca');[IO.File]::WriteAllText($token,'secret-fixture-token')
    $root=Join-Path $temp 'installed Agent'
    $options=@{Root=$root;Stage=$true;Version='v0.99.1';File=$binary;Checksums=$manifest}
    & $script install @options -NodeId node-one -Registry registry.test:9090 -Gateway gateway.test:9090 -CAFile $ca -TokenFile $token
    $entry=Join-Path $root 'bin\aiserveweave-agent.exe'
    if(!(Test-Path -LiteralPath $entry)){throw 'Installed binary is missing.'}
    $config=Join-Path $root 'state\agent.yaml';[IO.File]::WriteAllText($config,'existing configuration')
    $key=Join-Path $root 'state\identity\node.key';[IO.File]::WriteAllText($key,'node-identity')
    & $script install @options
    [IO.File]::WriteAllText($manifest,('0'*64)+"  $asset`n")
    $failed=$false
    try{& $script upgrade @options}catch{$failed=$true}
    if(!$failed){throw 'Bad digest was accepted.'}
    if((Get-FileHash -LiteralPath $entry).Hash -ne $digest){throw 'Failed verification replaced the active binary.'}
    & $script rollback -Root $root -Stage -Version v0.99.1
    & $script uninstall -Root $root -Stage
    if(Test-Path -LiteralPath $entry){throw 'Uninstall retained the active entry.'}
    if([IO.File]::ReadAllText($key) -ne 'node-identity' -or [IO.File]::ReadAllText($config) -ne 'existing configuration'){throw 'Identity/configuration was not preserved.'}
    Write-Output 'Windows staging tests passed.'
} finally {Remove-Item -LiteralPath $temp -Force -Recurse}
