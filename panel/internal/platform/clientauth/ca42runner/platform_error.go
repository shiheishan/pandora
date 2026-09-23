package ca42runner

import "errors"

var errUnsupportedPlatform = errors.New("CA42 root runner requires Linux amd64 or arm64")
