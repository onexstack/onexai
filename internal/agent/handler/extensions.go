// Copyright 2026 Lingfei Kong <colin404@foxmail.com>
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/onexstack/onexai/internal/agent/biz/v1/extensions"
	"github.com/onexstack/onexai/internal/pkg/errno"
)

// ExtensionKind describes one managed extension object for the console.
type ExtensionKind struct {
	Resource    string `json:"resource"`
	Kind        string `json:"kind"`
	Group       string `json:"group"`
	Version     string `json:"version"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
}

// ListExtensionKinds handles GET /v1/admin/extensions.
//
// The console builds its menu from this rather than from a copy kept in the
// frontend: the kinds this server can actually reach are the ones worth
// showing, and a kind that exists only in the frontend is a page that fails
// when opened.
func (h *Handler) ListExtensionKinds(c *gin.Context) {
	if h.extensions == nil {
		core.WriteResponse(c, nil, errno.ErrNotConfigured.WithMessage("the extension surface is not configured"))
		return
	}

	kinds := h.extensions.Kinds()
	out := make([]ExtensionKind, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, ExtensionKind{
			Resource:    k.Resource,
			Kind:        k.ObjectKind,
			Group:       k.Group,
			Version:     k.Version,
			DisplayName: k.DisplayName,
			Description: k.Description,
		})
	}
	core.WriteResponse(c, gin.H{"kinds": out}, nil)
}

// ListExtensions handles GET /v1/admin/extensions/:kind.
func (h *Handler) ListExtensions(c *gin.Context) {
	if h.extensions == nil {
		core.WriteResponse(c, nil, errno.ErrNotConfigured.WithMessage("the extension surface is not configured"))
		return
	}

	list, err := h.extensions.List(c.Request.Context(), c.Param("kind"))
	if err != nil {
		core.WriteResponse(c, nil, extensionError(err))
		return
	}
	core.WriteResponse(c, gin.H{"items": list.Items}, nil)
}

// GetExtension handles GET /v1/admin/extensions/:kind/:name.
func (h *Handler) GetExtension(c *gin.Context) {
	if h.extensions == nil {
		core.WriteResponse(c, nil, errno.ErrNotConfigured.WithMessage("the extension surface is not configured"))
		return
	}

	obj, err := h.extensions.Get(c.Request.Context(), c.Param("kind"), c.Param("name"))
	if err != nil {
		core.WriteResponse(c, nil, extensionError(err))
		return
	}
	core.WriteResponse(c, obj.Object, nil)
}

// CreateExtension handles POST /v1/admin/extensions/:kind.
func (h *Handler) CreateExtension(c *gin.Context) {
	if h.extensions == nil {
		core.WriteResponse(c, nil, errno.ErrNotConfigured.WithMessage("the extension surface is not configured"))
		return
	}

	obj, err := bindExtension(c)
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	created, err := h.extensions.Create(c.Request.Context(), c.Param("kind"), obj)
	if err != nil {
		core.WriteResponse(c, nil, extensionError(err))
		return
	}
	c.JSON(http.StatusCreated, created.Object)
}

// UpdateExtension handles PUT /v1/admin/extensions/:kind/:name.
func (h *Handler) UpdateExtension(c *gin.Context) {
	if h.extensions == nil {
		core.WriteResponse(c, nil, errno.ErrNotConfigured.WithMessage("the extension surface is not configured"))
		return
	}

	obj, err := bindExtension(c)
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	// The URL is the address; a body that disagrees would create a second way to
	// name the same object and let a typo write to the wrong one.
	if name := c.Param("name"); obj.GetName() != name {
		obj.SetName(name)
	}
	updated, err := h.extensions.Update(c.Request.Context(), c.Param("kind"), obj)
	if err != nil {
		core.WriteResponse(c, nil, extensionError(err))
		return
	}
	core.WriteResponse(c, updated.Object, nil)
}

// DeleteExtension handles DELETE /v1/admin/extensions/:kind/:name.
func (h *Handler) DeleteExtension(c *gin.Context) {
	if h.extensions == nil {
		core.WriteResponse(c, nil, errno.ErrNotConfigured.WithMessage("the extension surface is not configured"))
		return
	}

	if err := h.extensions.Delete(c.Request.Context(), c.Param("kind"), c.Param("name")); err != nil {
		core.WriteResponse(c, nil, extensionError(err))
		return
	}
	core.WriteResponse(c, gin.H{"deleted": c.Param("name")}, nil)
}

// bindExtension decodes a request body into an unstructured object.
func bindExtension(c *gin.Context) (*unstructured.Unstructured, error) {
	obj := &unstructured.Unstructured{}
	if err := c.ShouldBindJSON(obj); err != nil {
		return nil, errno.ErrBind.WithMessage("%s", err.Error())
	}
	return obj, nil
}

// extensionError maps an API server error onto the platform's error vocabulary,
// so the console sees 404 for a missing object, 409 for a stale write and 400
// for a request the API server rejected — not a blanket 500.
func extensionError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, extensions.ErrUnknownKind):
		return errno.ErrNotFound.WithMessage("%s", err.Error())
	case errors.Is(err, extensions.ErrNameRequired), errors.Is(err, extensions.ErrResourceVersionRequired):
		return errno.ErrInvalidArgument.WithMessage("%s", err.Error())
	case apierrors.IsNotFound(err):
		return errno.ErrNotFound.WithMessage("%s", err.Error())
	case apierrors.IsConflict(err):
		// Written by someone else since this page loaded it: re-read, re-apply.
		return errno.ErrConflict.WithMessage("%s", err.Error())
	case apierrors.IsAlreadyExists(err):
		// A create for a name that is taken.
		return errno.ErrAlreadyExists.WithMessage("%s", err.Error())
	case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
		return errno.ErrInvalidArgument.WithMessage("%s", err.Error())
	case apierrors.IsForbidden(err):
		return errno.ErrPermissionDenied.WithMessage("%s", err.Error())
	case apierrors.IsUnauthorized(err):
		return errno.ErrUnauthenticated.WithMessage("%s", err.Error())
	default:
		// Anything that is not an API status came from a layer that rejected the
		// request rather than from the API server failing.
		var status apierrors.APIStatus
		if !errors.As(err, &status) {
			return errno.ErrInvalidArgument.WithMessage("%s", err.Error())
		}
		return errno.ErrInternal.WithMessage("%s", err.Error())
	}
}

func init() {
	RegisterAdmin(func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		ext := engine.Group("/extensions", mws...)
		ext.GET("", h.ListExtensionKinds)
		ext.GET("/:kind", h.ListExtensions)
		ext.POST("/:kind", h.CreateExtension)
		ext.GET("/:kind/:name", h.GetExtension)
		ext.PUT("/:kind/:name", h.UpdateExtension)
		ext.DELETE("/:kind/:name", h.DeleteExtension)
	})
}
