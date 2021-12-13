ARCH   ?= amd64
OS     ?= linux

BINDIR := ./bin
FILE_PATTERN := 'html\|go\|Makefile\|css'

IMAGE_REPO = docker.io/charlieegan3/
TAG ?= $(shell git rev-parse HEAD)

BUILD_BASE_IMAGE ?= gcr.io/distroless/static:nonroot
KO_BASE_IMAGE ?= ko.local/distroless/static:nonroot
KO_PUSH_IMAGE := true
KO_PLATFORM ?= $(OS)/$(ARCH)

dev_server:
	find . | grep $(FILE_PATTERN) | entr -r bash -c 'clear; go run main.go server --config config.dev.yaml'

image: depend ko-base-image
	KO_DOCKER_REPO=$(IMAGE_REPO) $(BINDIR)/ko publish \
		--base-import-paths \
		--push=$(KO_PUSH_IMAGE) \
		--platform=$(KO_PLATFORM) \
		--tags $(TAG),latest \
		.

ko-base-image: depend
	docker pull $(BUILD_BASE_IMAGE)

	if [ -z "$(shell docker images -q $(KO_BASE_IMAGE))" ]; then \
		docker tag $(BUILD_BASE_IMAGE) $(KO_BASE_IMAGE); \
	fi

depend: $(BINDIR)/ko

$(BINDIR)/ko:
	# The ko project doesn't support installation via go/go install as the
	# above projects do. https://github.com/google/ko/issues/258
	# use uname since that's what the GH releases seem to be based on
	VERSION=0.9.3 && curl -L https://github.com/google/ko/releases/download/v$${VERSION}/ko_$${VERSION}_$(shell uname)_$(shell uname -m).tar.gz | tar xzf - ko
	chmod +x ./ko
	mv ./ko $(BINDIR)
