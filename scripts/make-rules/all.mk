# ==============================================================================
# Makefile used to aggregate all makefiles for easy management.
#

# Include all .mk files in all.mk for easy management.
include scripts/make-rules/common.mk
include scripts/make-rules/tools.mk # include at second order
include scripts/make-rules/golang.mk
include scripts/make-rules/generate.mk
include scripts/make-rules/image.mk
