#!/bin/bash

export GOPATH="$HOME/go/pkg/1.18"
export GOROOT="$HOME/go/root/1.18.4"
export PATH="$PATH:$GOROOT/bin"
export GO111MODULE=on


exec code .
