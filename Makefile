BEATNAME=cloudtrailbeat
BEAT_DIR=github.com/aidan-/cloudtrailbeat
ES_BEATS=./vendor/github.com/elastic/beats
GOPACKAGES=$(shell glide novendor)
SYSTEM_TESTS=false

# Only crosscompile for linux because other OS'es use cgo.
#GOX_OS=linux darwin windows solaris freebsd netbsd openbsd
GOX_OS=linux

include $(ES_BEATS)/libbeat/scripts/Makefile
