##
## Protobuf compilation
##
P_TIMESTAMP = Mgoogle/protobuf/timestamp.proto=github.com/golang/protobuf/ptypes/timestamp

PKGMAP = $(P_TIMESTAMP)

.PHONY: protos
protos:
	cd pb && PATH=$(PATH):$(GOPATH)/bin protoc --go_out=$(PKGMAP):./ *.proto
