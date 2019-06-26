TAG ?= $(shell cat ../../VERSION | sed 's/v\([0-9]*\.[0-9]*\)\.\([0-9]*\)/\1/')
MINOR_TAG ?= $(shell cat ../../VERSION | sed 's/v\([0-9]*\.[0-9]*\.[0-9]*\)/\1/')

# Convenience makefiles.
include gcloud.Makefile
include var.Makefile

# app.Makefile provides the main targets for installing the
# application.
# It requires several APP_* variables defined as followed.
include app.Makefile

APP_DEPLOYER_IMAGE ?= $(REGISTRY)/marketplace/csi-plugin/deployer:$(TAG)
NAME ?= csi-plugin-1
APP_PARAMETERS ?= { \
  "name": "$(NAME)", \
  "namespace": "$(NAMESPACE)", \
  "imageHammerspaceCsi": "$(REGISTRY)/marketplace/csi-plugin:$(TAG)", \
}

TESTER_IMAGE ?= $(REGISTRY)/marketplace/csi-plugin/tester:$(TAG)
APP_TEST_PARAMETERS ?= { \
  "imageTester": "$(TESTER_IMAGE)" \
}

# Extend the target as defined in app.Makefile to
# include real dependencies.
app/build:: .build/csi-plugin/deployer \
            .build/csi-plugin/tester \
            .build/csi-plugin/csi-plugin

.build/csi-plugin: | .build
	mkdir -p "$@"

.build/csi-plugin/deployer: .build/var/APP_DEPLOYER_IMAGE \
                            .build/var/MARKETPLACE_TOOLS_TAG \
                            .build/var/REGISTRY \
                            .build/var/TAG \
                            apptest/deployer/* \
                            apptest/deployer/manifest/* \
                            deployer/* \
                            manifest/* \
                            schema.yaml \
                            | .build/csi-plugin
	$(call print_target, $@)
	docker build \
	    --build-arg REGISTRY="$(REGISTRY)/marketplace/csi-plugin" \
	    --build-arg TAG="$(TAG)" \
	    --build-arg MARKETPLACE_TOOLS_TAG="$(MARKETPLACE_TOOLS_TAG)" \
	    --tag "$(APP_DEPLOYER_IMAGE)" \
	    -f deployer/Dockerfile \
	    .
	docker push "$(APP_DEPLOYER_IMAGE)"
	@touch "$@"

.build/csi-plugin/tester: .build/var/TESTER_IMAGE
	$(call print_target, $@)
	docker pull alpine
	docker tag alpine "$(TESTER_IMAGE)"
	docker push "$(TESTER_IMAGE)"
	@touch "$@"

# Simulate building of primary app image. Actually just copying public image to
# local registry.
.build/csi-plugin/csi-plugin: .build/var/REGISTRY \
                              .build/var/TAG \
                              | .build/csi-plugin
	$(call print_target, $@)
	docker pull hammerspaceinc/csi-plugin:v0.1.3
	docker tag hammerspaceinc/csi-plugin:v0.1.3 "$(REGISTRY)/marketplace/csi-plugin:$(TAG)"
	docker tag hammerspaceinc/csi-plugin:v0.1.3 "$(REGISTRY)/marketplace/csi-plugin:$(MINOR_TAG)"
	docker tag hammerspaceinc/csi-plugin:v0.1.3 "$(REGISTRY)/marketplace/csi-plugin:latest"
	docker push "$(REGISTRY)/marketplace/csi-plugin:$(TAG)"
	docker push "$(REGISTRY)/marketplace/csi-plugin:$(MINOR_TAG)"
	docker push "$(REGISTRY)/marketplace/csi-plugin:latest"
	@touch "$@"
