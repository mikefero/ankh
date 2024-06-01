.DEFAULT_GOAL := test

# Ensure golang is available
ifeq (, $(shell which go 2> /dev/null))
$(error "'go' is not installed or available in PATH")
endif

APP_DIR := $(patsubst %/,%,$(dir $(abspath $(lastword $(MAKEFILE_LIST)))))
APP_WORKDIR := $(shell pwd)

include $(APP_DIR)/mk/test.mk
include $(APP_DIR)/mk/tools.mk
