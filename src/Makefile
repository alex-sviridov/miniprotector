BINARY_DIR := ../bin

# Go build flags
LDFLAGS := -ldflags "-s -w"
BUILDFLAGS := -trimpath

.PHONY: all build clean

all: build 

# Build the binaries
build:
	CGO_ENABLED=1 GOOS=linux go build $(BUILDFLAGS) $(LDFLAGS) -o $(BINARY_DIR)/brfs brfs/*.go
	CGO_ENABLED=1 GOOS=linux go build $(BUILDFLAGS) $(LDFLAGS) -o $(BINARY_DIR)/bwfs bwfs/*.go

# Cleanup
clean:
	-rm $(BINARY_DIR)/*