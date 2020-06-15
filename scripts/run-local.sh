#!/usr/bin/env bash

go env -w GOPROXY=direct
go env -w GOSUMDB=off

go run ./cmd/aurora-servicediscovery/ \
    2>&1 | tee a.log