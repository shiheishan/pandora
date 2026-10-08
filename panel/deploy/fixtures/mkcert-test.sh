#!/usr/bin/env bash
# 桩测试用的证书工厂（edge-tls_mock_test.sh、healthcheck_mock_test.sh）：现签一张自签证书。
#   mkcert-test.sh <cert> <key> <CN> <SAN> <天数>
#   mkcert-test.sh <cert> <key> <CN> <SAN> window <起 YYYYMMDDHHMMSSZ> <止 YYYYMMDDHHMMSSZ>
# 指定起止要 openssl ca -selfsign：req 的 -not_before / -not_after 到 OpenSSL 3.4 才有，CI 上是 3.0。
set -euo pipefail
cert="$1" key="$2" cn="$3" san="$4"
mkdir -p "$(dirname "$cert")" "$(dirname "$key")"
if [ "$5" != window ]; then
  exec openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -keyout "$key" -out "$cert" \
    -days "$5" -subj "/CN=$cn" -addext "subjectAltName=$san" >/dev/null 2>&1
fi
w="$(mktemp -d)"
trap 'rm -rf -- "$w"' EXIT
mkdir -p "$w/new"
: >"$w/index.txt"
echo 01 >"$w/serial"
cat >"$w/ca.cnf" <<CNF
[ca]
default_ca = test_ca
[test_ca]
database = $w/index.txt
new_certs_dir = $w/new
serial = $w/serial
default_md = sha256
policy = any
copy_extensions = copy
unique_subject = no
[any]
commonName = supplied
CNF
openssl req -new -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -keyout "$key" -out "$w/req.csr" \
  -subj "/CN=$cn" -addext "subjectAltName=$san" >/dev/null 2>&1
openssl ca -batch -selfsign -config "$w/ca.cnf" -keyfile "$key" -in "$w/req.csr" -out "$w/cert.pem" \
  -startdate "$6" -enddate "$7" >/dev/null 2>&1
openssl x509 -in "$w/cert.pem" -out "$cert"
