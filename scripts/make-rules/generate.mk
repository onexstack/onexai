# ==============================================================================
# Makefile used for code generation
#

gen.add-copyright: tools.verify.addlicense ## Add copyright headers.
	@addlicense -v -f $(PROJ_ROOT_DIR)/scripts/boilerplate.txt $(PROJ_ROOT_DIR) --skip-dirs=third_party,vendor,$(OUTPUT_DIR)

gen.protoc: tools.verify.protoc-gen-go ## Compile protobuf files.
	@echo "===========> Generate protobuf files"
	@mkdir -p $(PROJ_ROOT_DIR)/api/openapi
	@protoc                                            \
		--proto_path=$(APIROOT)                          \
		--proto_path=$(PROJ_ROOT_DIR)/third_party/protobuf             \
		--go_out=paths=source_relative:$(APIROOT)        \
		--go-grpc_out=paths=source_relative:$(APIROOT)   \
		--openapiv2_out=$(PROJ_ROOT_DIR)/api/openapi \
		--openapiv2_opt=allow_delete_body=true,logtostderr=true \
		--defaults_out=paths=source_relative:$(APIROOT) \
		$(shell find $(APIROOT)/* -name *.proto)
	@find $(APIROOT)/* -name "*.pb.go" -exec protoc-go-inject-tag -input={} \;

gen.protoc.%: tools.verify.protoc-gen-go ## Compile protobuf files.
	@echo "===========> Generate protobuf files"
	@mkdir -p $(PROJ_ROOT_DIR)/api/openapi
	@protoc                                            \
		--proto_path=$(APIROOT)                          \
		--proto_path=$(PROJ_ROOT_DIR)/third_party/protobuf             \
		--go_out=paths=source_relative:$(APIROOT)        \
		--go-grpc_out=paths=source_relative:$(APIROOT)   \
		--openapiv2_out=$(PROJ_ROOT_DIR)/api/openapi \
		--openapiv2_opt=allow_delete_body=true,logtostderr=true \
		--defaults_out=paths=source_relative:$(APIROOT) \
		$(shell find $(APIROOT)/$* -name *.proto)
	@find $(APIROOT)/$* -name "*.pb.go" -exec protoc-go-inject-tag -input={} \;

gen.generate:
	@GOWORK=off go generate ./...

gen.k8s: ## Regenerate Kubernetes codegen artifacts (deepcopy/clientset/informers/listers/applyconfigurations/openapi).
	@bash $(PROJ_ROOT_DIR)/scripts/update-codegen.sh

gen.verify-codegen: ## Verify generated artifacts match the source; fail if they differ.
	@bash $(PROJ_ROOT_DIR)/scripts/verify-codegen.sh

# Phony targets (prevent conflicts between files and target names)
.PHONY: gen.add-copyright gen.protoc gen.generate gen.k8s gen.verify-codegen
