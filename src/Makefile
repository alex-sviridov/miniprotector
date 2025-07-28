BINARY_NAMES := brfs bwfs
BINARY_DIR := ../bin

# Go build flags
LDFLAGS := -ldflags "-s -w"
BUILDFLAGS := -trimpath

.PHONY: all build clean

all: build

# Build the binaries
build:
	@mkdir -p $(BINARY_DIR)
	@for binary in $(BINARY_NAMES); do \
		echo "Building $$binary..."; \
		CGO_ENABLED=1 GOOS=linux go build $(BUILDFLAGS) $(LDFLAGS) -o $(BINARY_DIR)/$$binary $$binary/*.go; \
		echo "Binary built at $(BINARY_DIR)/$$binary"; \
	done

clean:
	rm -rf $(BINARY_DIR)