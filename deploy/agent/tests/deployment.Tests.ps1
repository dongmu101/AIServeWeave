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
    # Commit builds install only from local verified artifacts, with the same rules as agent.sh.
    # 提交号构建只从本地已校验制品安装，规则与 agent.sh 一致。
    $commit='763e0a2'
    $commitAsset="aiserveweave-agent_${commit}_windows_$arch.exe"
    $commitBinary=Join-Path $temp $commitAsset;Copy-Item -LiteralPath $binary -Destination $commitBinary
    $commitSums=Join-Path $temp 'commit-sums.txt';[IO.File]::WriteAllText($commitSums,"$digest  $commitAsset`n")
    $commitRoot=Join-Path $temp 'commit Agent'
    & $script install -Root $commitRoot -Stage -Version $commit -File $commitBinary -Checksums $commitSums -NodeId node-commit -Registry registry.test:9090 -Gateway gateway.test:9090 -CAFile $ca -TokenFile $token
    if(!(Test-Path -LiteralPath (Join-Path $commitRoot 'bin\aiserveweave-agent.exe'))){throw 'Commit install is missing.'}
    $rejected=@(
        @{Name='download';Arguments=@{Version=$commit;BaseUrl='https://example.invalid/releases/763e0a2'}},
        @{Name='no checksums';Arguments=@{Version=$commit;File=$commitBinary}},
        @{Name='dirty';Arguments=@{Version="$commit-dirty";File=$commitBinary;Checksums=$commitSums}},
        @{Name='development';Arguments=@{Version='dev';File=$commitBinary;Checksums=$commitSums}},
        @{Name='uppercase';Arguments=@{Version='763E0A2';File=$commitBinary;Checksums=$commitSums}},
        @{Name='short';Arguments=@{Version='763e0a';File=$commitBinary;Checksums=$commitSums}},
        @{Name='long';Arguments=@{Version=('a'*41);File=$commitBinary;Checksums=$commitSums}}
    )
    foreach($case in $rejected){
        $failed=$false;$rejectedRoot=Join-Path $temp ('rejected '+$case.Name);$arguments=$case.Arguments
        try{& $script install -Root $rejectedRoot -Stage @arguments}catch{$failed=$true}
        if(!$failed){throw "Version case '$($case.Name)' was accepted."}
        if(Test-Path -LiteralPath (Join-Path $rejectedRoot 'releases')){throw "Version case '$($case.Name)' left a release behind."}
    }
    Write-Output 'Windows staging tests passed.'
} finally {Remove-Item -LiteralPath $temp -Force -Recurse}
