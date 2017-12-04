# (C) Copyright 2017 Hewlett Packard Enterprise Development LP
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

GO              ?= GO15VENDOREXPERIMENT=1 go
GOPATH          := $(firstword $(subst :, ,$(shell $(GO) env GOPATH)))
PROMU           ?= $(GOPATH)/bin/promu
GOLINTER        ?= $(GOPATH)/bin/gometalinter
#aligncheck and gosimple took unfortunately too long at travisCI
GOLINTER_OPT    ?= --vendor --deadline 6m --cyclo-over=20 --disable=aligncheck --disable=gosimple --disable=gotype
pkgs            = $(shell $(GO) list ./... | grep -v /vendor/)
TARGET          ?= lustre_exporter

PREFIX          ?= $(shell pwd)
BIN_DIR         ?= $(shell pwd)

all: format vet gometalinter build test

test:
	@echo ">> running tests"
	@$(GO) test -short $(pkgs)

format:
	@echo ">> formatting code"
	@$(GO) fmt $(pkgs)

gometalinter: $(GOLINTER)
	@echo ">> linting code"
	@$(GOLINTER) --install --update > /dev/null
	@$(GOLINTER) $(GOLINTER_OPT) ./...

build: $(PROMU)
	@echo ">> building binaries"
	@$(PROMU) build --prefix $(PREFIX)

clean:
	@echo ">> Cleaning up"
	@$(RM) $(TARGET)

$(GOPATH)/bin/promu promu:
	@GOOS=$(shell uname -s | tr A-Z a-z) \
		GOARCH=$(subst x86_64,amd64,$(patsubst i%86,386,$(shell uname -m))) \
		$(GO) get -u github.com/prometheus/promu

$(GOPATH)/bin/gometalinter lint:
	@GOOS=$(shell uname -s | tr A-Z a-z) \
		GOARCH=$(subst x86_64,amd64,$(patsubst i%86,386,$(shell uname -m))) \
		$(GO) get -u github.com/alecthomas/gometalinter

.PHONY: all format vet build test promu clean $(GOPATH)/bin/promu $(GOPATH)/bin/gometalinter lint
