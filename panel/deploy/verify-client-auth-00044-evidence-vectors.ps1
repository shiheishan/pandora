[CmdletBinding()]
param(
    [switch]$EmitActual
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$RepoRoot = Split-Path -Parent $PSScriptRoot
$VectorRoot = Join-Path $RepoRoot 'internal\platform\clientauth\evidencecodec\testdata\evidence_v1'
$Logical = Get-Content -LiteralPath (Join-Path $VectorRoot 'logical_vectors.json') -Raw -Encoding UTF8 | ConvertFrom-Json
$Expected = Get-Content -LiteralPath (Join-Path $VectorRoot 'expected_vectors.json') -Raw -Encoding UTF8 | ConvertFrom-Json
$Utf8 = New-Object System.Text.UTF8Encoding($false, $true)
$ContractPath = Join-Path $RepoRoot '.ai-company\handoffs\client-auth-00044-classify-backfill-contract-20260731.md'
$ContractHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $ContractPath).Hash.ToLowerInvariant()
if ($ContractHash -cne [string]$Logical.contract_sha256) {
    throw "CONTRACT_SHA256_MISMATCH expected=$($Logical.contract_sha256) actual=$ContractHash"
}

function New-Bytes { New-Object 'System.Collections.Generic.List[byte]' }
function Add-Raw([System.Collections.Generic.List[byte]]$Out, [byte[]]$Bytes) { $Out.AddRange($Bytes) }
function Add-U16([System.Collections.Generic.List[byte]]$Out, [uint16]$Value) {
    $b = [BitConverter]::GetBytes($Value); [Array]::Reverse($b); Add-Raw $Out $b
}
function Add-U32([System.Collections.Generic.List[byte]]$Out, [uint32]$Value) {
    $b = [BitConverter]::GetBytes($Value); [Array]::Reverse($b); Add-Raw $Out $b
}
function Add-I32([System.Collections.Generic.List[byte]]$Out, [int32]$Value) {
    $b = [BitConverter]::GetBytes($Value); [Array]::Reverse($b); Add-Raw $Out $b
}
function Add-U64([System.Collections.Generic.List[byte]]$Out, [uint64]$Value) {
    $b = [BitConverter]::GetBytes($Value); [Array]::Reverse($b); Add-Raw $Out $b
}
function Add-I64([System.Collections.Generic.List[byte]]$Out, [int64]$Value) {
    $b = [BitConverter]::GetBytes($Value); [Array]::Reverse($b); Add-Raw $Out $b
}
function Add-String16([System.Collections.Generic.List[byte]]$Out, [string]$Value) {
    $b = $Utf8.GetBytes($Value)
    if ($b.Length -gt [uint16]::MaxValue) { throw 'string overflow' }
    Add-U16 $Out ([uint16]$b.Length); Add-Raw $Out $b
}
function Add-Bytes64([System.Collections.Generic.List[byte]]$Out, [byte[]]$Value) {
    Add-U64 $Out ([uint64]$Value.Length); Add-Raw $Out $Value
}
function Hex-ToBytes([string]$Hex) {
    if (($Hex.Length % 2) -ne 0 -or $Hex -notmatch '^[0-9a-f]*$') { throw "invalid lowercase hex" }
    $out = [byte[]]::new($Hex.Length / 2)
    for ($i = 0; $i -lt $out.Length; $i++) { $out[$i] = [Convert]::ToByte($Hex.Substring($i * 2, 2), 16) }
    return ,$out
}
function Bytes-ToHex([byte[]]$Bytes) { (($Bytes | ForEach-Object { $_.ToString('x2') }) -join '') }
function SHA256-Hex([byte[]]$Bytes) {
    $h = [System.Security.Cryptography.SHA256]::Create()
    try { Bytes-ToHex $h.ComputeHash($Bytes) } finally { $h.Dispose() }
}
function HMAC-Hex([byte[]]$Key, [byte[]]$Bytes) {
    $h = New-Object System.Security.Cryptography.HMACSHA256
    try { $h.Key = $Key; Bytes-ToHex $h.ComputeHash($Bytes) } finally { $h.Dispose() }
}
function Add-Identity([System.Collections.Generic.List[byte]]$Out, [string]$Name) {
    Add-String16 $Out 'pg_catalog'; Add-String16 $Out $Name
}

$Columns = @(
    @{att=1; name='id'; type='uuid'; tag=5; nullable=0; typmod=-1; elem=0; elemtypmod=-1},
    @{att=2; name='tenant_id'; type='uuid'; tag=5; nullable=0; typmod=-1; elem=0; elemtypmod=-1},
    @{att=3; name='nullable_text'; type='text'; tag=7; nullable=1; typmod=-1; elem=0; elemtypmod=-1},
    @{att=4; name='observed_at'; type='timestamptz'; tag=9; nullable=0; typmod=6; elem=0; elemtypmod=-1},
    @{att=5; name='amount'; type='numeric'; tag=12; nullable=0; typmod=-1; elem=0; elemtypmod=-1},
    @{att=6; name='document'; type='jsonb'; tag=13; nullable=0; typmod=-1; elem=0; elemtypmod=-1},
    @{att=7; name='country'; type='bpchar'; tag=15; nullable=0; typmod=6; elem=0; elemtypmod=-1},
    @{att=8; name='networks'; type='_inet'; tag=14; nullable=0; typmod=-1; elem=16; elemtypmod=-1},
    @{att=9; name='labels'; type='_text'; tag=14; nullable=0; typmod=-1; elem=7; elemtypmod=-1}
)

function Encode-Manifest {
    $o = New-Bytes; Add-Raw $o $Utf8.GetBytes("pandora-client-auth-00044-relation-manifest-v1`0")
    Add-U32 $o 1; Add-String16 $o 'public'; Add-String16 $o 'vector_evidence'; $o.Add(1); Add-U32 $o $Columns.Count
    foreach ($c in $Columns) {
        Add-U16 $o $c.att; Add-String16 $o $c.name; Add-Identity $o $c.type; Add-Identity $o $c.type
        $o.Add([byte]$c.tag); $o.Add([byte]$c.nullable); Add-I32 $o $c.typmod; $o.Add([byte]$c.elem); Add-I32 $o $c.elemtypmod
    }
    return ,$o.ToArray()
}
function Encode-Inet([int]$Family, [int]$Prefix, [string]$AddressHex) {
    $address = Hex-ToBytes $AddressHex
    if (($Family -eq 4 -and ($Prefix -gt 32 -or $address.Length -ne 4)) -or
        ($Family -eq 6 -and ($Prefix -gt 128 -or $address.Length -ne 16))) { throw 'inet invalid' }
    $o=New-Bytes; $o.Add([byte]$Family); $o.Add([byte]$Prefix); Add-Raw $o $address; return ,($o.ToArray())
}
function Encode-Array([int]$Tag, [object[]]$Dimensions, [object[]]$Elements, [scriptblock]$Encoder) {
    $o=New-Bytes; $o.Add([byte]$Tag); Add-U32 $o $Dimensions.Count
    $product = if ($Dimensions.Count -eq 0) { 0L } else { 1L }
    foreach($d in $Dimensions) { Add-I32 $o $d.length; Add-I32 $o $d.lower_bound; $product *= $d.length }
    if ($product -ne $Elements.Count) { throw 'array count mismatch' }
    Add-U64 $o $Elements.Count
    foreach($e in $Elements) {
        if ($e.null) { $o.Add(0); continue }
        $o.Add(1); [byte[]]$value = & $Encoder $e; Add-Bytes64 $o $value
    }
    return ,$o.ToArray()
}
function Encode-JSON {
    # Independent construction of {"a":12.34,"z":[null,true,"中"]}.
    $number=New-Bytes; $number.Add(3); Add-Bytes64 $number ($Utf8.GetBytes($Logical.numeric))
    $nullNode=[byte[]](0); $trueNode=[byte[]](2)
    $str=New-Bytes; $str.Add(4); Add-Bytes64 $str ($Utf8.GetBytes([string][char]0x4e2d))
    $arr=New-Bytes; $arr.Add(5); Add-U64 $arr 3; Add-Bytes64 $arr $nullNode; Add-Bytes64 $arr $trueNode; Add-Bytes64 $arr ($str.ToArray())
    $obj=New-Bytes; $obj.Add(6); Add-U64 $obj 2
    Add-Bytes64 $obj ($Utf8.GetBytes('a')); Add-Bytes64 $obj ($number.ToArray())
    Add-Bytes64 $obj ($Utf8.GetBytes('z')); Add-Bytes64 $obj ($arr.ToArray())
    return ,($obj.ToArray())
}
function Add-Field([System.Collections.Generic.List[byte]]$Out,[string]$Name,[int]$Tag,[bool]$IsNull,[byte[]]$Value) {
    Add-String16 $Out $Name; $Out.Add([byte]$Tag)
    if($IsNull){$Out.Add(0)} else {$Out.Add(1); Add-Bytes64 $Out $Value}
}

$bpchar = $Utf8.GetBytes($Logical.bpchar_raw)
$ipv4 = Encode-Inet $Logical.ipv4.family $Logical.ipv4.prefix $Logical.ipv4.address_hex
$ipv6 = Encode-Inet $Logical.ipv6.family $Logical.ipv6.prefix $Logical.ipv6.address_hex
$emptyArray = Encode-Array 7 @() @() { param($e) $Utf8.GetBytes([string]$e.text) }
$textArray = Encode-Array 7 @($Logical.text_array.dimensions) @($Logical.text_array.elements) { param($e) $Utf8.GetBytes([string]$e.text) }
$inetElements = @(@{null=$false; bytes=$ipv4},@{null=$false; bytes=$ipv6})
$inetArray = Encode-Array 16 @(@{length=2;lower_bound=1}) $inetElements { param($e) [byte[]]$e.bytes }
$jsonb = Encode-JSON
$id = Hex-ToBytes $Logical.row_uuid_hex

$row=New-Bytes; Add-Raw $row ($Utf8.GetBytes("pandora-client-auth-00044-row-v1`0"))
Add-String16 $row 'public'; Add-String16 $row 'vector_evidence'; Add-U32 $row 9
Add-Field $row 'id' 5 $false $id
Add-Field $row 'tenant_id' 5 $false (Hex-ToBytes $Logical.tenant_uuid_hex)
Add-Field $row 'nullable_text' 7 $true ([byte[]]::new(0))
$timestamp=New-Bytes; Add-I64 $timestamp ([int64]$Logical.timestamp_microseconds)
Add-Field $row 'observed_at' 9 $false $timestamp.ToArray()
Add-Field $row 'amount' 12 $false ($Utf8.GetBytes($Logical.numeric))
Add-Field $row 'document' 13 $false $jsonb
Add-Field $row 'country' 15 $false $bpchar
Add-Field $row 'networks' 14 $false $inetArray
Add-Field $row 'labels' 14 $false $textArray
$rowBytes=$row.ToArray()

$key=Hex-ToBytes $Logical.hmac_key_hex
$emptyMessage=New-Bytes; Add-Raw $emptyMessage ($Utf8.GetBytes("pandora-client-auth-00044-rowset-v1`0")); Add-U64 $emptyMessage 0
$rowset=New-Bytes; Add-Raw $rowset ($Utf8.GetBytes("pandora-client-auth-00044-rowset-v1`0")); Add-U64 $rowset 1
Add-String16 $rowset 'public'; Add-String16 $rowset 'vector_evidence'; Add-Bytes64 $rowset $rowBytes
function Artifact-Message([byte[]]$Artifact) {
    $m=New-Bytes
    Add-Raw $m ($Utf8.GetBytes("pandora-client-auth-00044-classification-artifact-hmac-v1`0"))
    Add-String16 $m 'artifact-2026-01'
    Add-String16 $m 'pandora-device-key-classification-v1'
    Add-Bytes64 $m $Artifact
    return ,($m.ToArray())
}

$Actual=[ordered]@{
    manifest_sha256 = SHA256-Hex (Encode-Manifest)
    empty_rowset_hmac = HMAC-Hex $key $emptyMessage.ToArray()
    bpchar_hex = Bytes-ToHex $bpchar
    ipv4_hex = Bytes-ToHex $ipv4
    ipv6_hex = Bytes-ToHex $ipv6
    empty_array_hex = Bytes-ToHex $emptyArray
    text_array_hex = Bytes-ToHex $textArray
    jsonb_hex = Bytes-ToHex $jsonb
    row_hex = Bytes-ToHex $rowBytes
    row_sha256 = SHA256-Hex $rowBytes
    rowset_hmac = HMAC-Hex $key $rowset.ToArray()
    artifact_empty_message_hex = Bytes-ToHex (Artifact-Message ([byte[]]::new(0)))
    artifact_empty_hmac = HMAC-Hex $key (Artifact-Message ([byte[]]::new(0)))
    artifact_object_lf_message_hex = Bytes-ToHex (Artifact-Message ([byte[]](0x7b,0x7d,0x0a)))
    artifact_object_lf_hmac = HMAC-Hex $key (Artifact-Message ([byte[]](0x7b,0x7d,0x0a)))
}
if($EmitActual){$Actual|ConvertTo-Json; exit 0}
foreach($name in $Actual.Keys){
    if([string]$Expected.$name -cne [string]$Actual[$name]){throw "VECTOR_MISMATCH:$name expected=$($Expected.$name) actual=$($Actual[$name])"}
}
'PASS: CLIENT-AUTH-00044 PowerShell independent evidence vectors'
