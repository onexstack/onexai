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
	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	memorysvc "github.com/onexstack/onexai/internal/agent/pkg/memory"
	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
	"github.com/onexstack/onexai/internal/pkg/errno"
	"github.com/onexstack/onexai/internal/pkg/restbind"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// CreateMemory handles POST /v1/agent/memories.
func (h *Handler) CreateMemory(c *gin.Context) {
	core.HandleJSONRequest(c, h.biz.MemoryV1().CreateMemory, h.val.ValidateCreateMemoryRequest)
}

// GetMemory handles GET /v1/agent/memories/:memory.
func (h *Handler) GetMemory(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.MemoryV1().GetMemory, h.val.ValidateGetMemoryRequest)
}

// ListMemory handles GET /v1/agent/memories.
func (h *Handler) ListMemory(c *gin.Context) {
	core.HandleQueryRequest(c, h.biz.MemoryV1().ListMemory, h.val.ValidateListMemoryRequest)
}

// UpdateMemory handles PUT /v1/agent/memories/:memory.
func (h *Handler) UpdateMemory(c *gin.Context) {
	core.HandleRequest(c, restbind.JSONWithPath(c, func(rq *v1.UpdateMemoryRequest) {
		if rq.Memory != nil {
			rq.Memory.Name = c.Param("memory")
		}
	}), h.biz.MemoryV1().UpdateMemory, h.val.ValidateUpdateMemoryRequest)
}

// DeleteMemory handles DELETE /v1/agent/memories/:memory.
func (h *Handler) DeleteMemory(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.MemoryV1().DeleteMemory, h.val.ValidateDeleteMemoryRequest)
}

// CreateMemoryEntry handles POST /v1/user/memories/:memory/entries.
func (h *Handler) CreateMemoryEntry(c *gin.Context) {
	h.createMemoryEntry(c, true)
}

// AdminCreateMemoryEntry handles POST /v1/admin/agent/memories/:memory/entries.
func (h *Handler) AdminCreateMemoryEntry(c *gin.Context) { h.createMemoryEntry(c, false) }

// SignedCreateMemoryEntry handles POST /v1/agent/memories/:memory/entries.
func (h *Handler) SignedCreateMemoryEntry(c *gin.Context) { h.createMemoryEntry(c, false) }

func (h *Handler) createMemoryEntry(c *gin.Context, forceSubject bool) {
	core.HandleRequest(c, restbind.JSONWithPath(c, func(rq *v1.CreateMemoryEntryRequest) {
		rq.Memory = c.Param("memory")
		if rq.Entry == nil {
			return
		}
		if forceSubject {
			caller := claimsSubject(c)
			// A browser user may only write their own bucket. The server never
			// trusts the body's subject; a forged one is refused rather than
			// silently rewritten, so the caller learns the write did not go where
			// they asked.
			if rq.Entry.Subject != "" && rq.Entry.Subject != caller {
				rq.Entry.Subject = ""
				return
			}
			rq.Entry.Subject = caller
		} else if rq.Entry.Actor == "" {
			// The admin route knows the operator even though it writes another
			// person's bucket; the audit should name the operator, not the subject.
			rq.Entry.Actor = claimsSubject(c)
		}
	}), h.biz.MemoryV1().CreateEntry, h.val.ValidateCreateMemoryEntryRequest)
}

// GetMemoryEntry handles GET /v1/user/memories/:memory/entries/:id.
func (h *Handler) GetMemoryEntry(c *gin.Context) { h.getMemoryEntry(c, true) }

// AdminGetMemoryEntry handles GET /v1/admin/agent/memories/:memory/entries/:id.
func (h *Handler) AdminGetMemoryEntry(c *gin.Context) { h.getMemoryEntry(c, false) }

// SignedGetMemoryEntry handles GET /v1/agent/memories/:memory/entries/:id.
func (h *Handler) SignedGetMemoryEntry(c *gin.Context) { h.getMemoryEntry(c, false) }

func (h *Handler) getMemoryEntry(c *gin.Context, forceSubject bool) {
	withAdminCaller(c, forceSubject)
	core.HandleRequest(c, restbind.URIWith(c, func(rq *v1.GetMemoryEntryRequest) {
		rq.Subject = entrySubject(c, forceSubject)
	}), h.biz.MemoryV1().GetEntry, h.val.ValidateGetMemoryEntryRequest)
}

// ListMemoryEntries handles GET /v1/user/memories/:memory/entries.
func (h *Handler) ListMemoryEntries(c *gin.Context) { h.listMemoryEntries(c, true) }

// AdminListMemoryEntries handles GET /v1/admin/agent/memories/:memory/entries.
func (h *Handler) AdminListMemoryEntries(c *gin.Context) { h.listMemoryEntries(c, false) }

// SignedListMemoryEntries handles GET /v1/agent/memories/:memory/entries.
func (h *Handler) SignedListMemoryEntries(c *gin.Context) { h.listMemoryEntries(c, false) }

func (h *Handler) listMemoryEntries(c *gin.Context, forceSubject bool) {
	withAdminCaller(c, forceSubject)
	core.HandleRequest(c, queryWith(c, func(rq *v1.ListMemoryEntriesRequest) {
		rq.Memory = c.Param("memory")
		if forceSubject {
			rq.Subject = claimsSubject(c)
		} else if rq.Subject == "" {
			rq.Subject = c.Query("subject")
		}
	}), h.biz.MemoryV1().ListEntries, h.val.ValidateListMemoryEntriesRequest)
}

// UpdateMemoryEntry handles PATCH /v1/user/memories/:memory/entries/:id.
func (h *Handler) UpdateMemoryEntry(c *gin.Context) { h.updateMemoryEntry(c, true) }

// AdminUpdateMemoryEntry handles PATCH /v1/admin/agent/memories/:memory/entries/:id.
func (h *Handler) AdminUpdateMemoryEntry(c *gin.Context) { h.updateMemoryEntry(c, false) }

// SignedUpdateMemoryEntry handles PATCH /v1/agent/memories/:memory/entries/:id.
func (h *Handler) SignedUpdateMemoryEntry(c *gin.Context) { h.updateMemoryEntry(c, false) }

func (h *Handler) updateMemoryEntry(c *gin.Context, forceSubject bool) {
	withAdminCaller(c, forceSubject)
	core.HandleRequest(c, restbind.JSONWithPath(c, func(rq *v1.UpdateMemoryEntryRequest) {
		rq.Memory = c.Param("memory")
		rq.Id = c.Param("id")
		if forceSubject {
			rq.Subject = claimsSubject(c)
		} else if rq.Subject == "" {
			rq.Subject = c.Query("subject")
		}
	}), h.biz.MemoryV1().UpdateEntry, h.val.ValidateUpdateMemoryEntryRequest)
}

// DeleteMemoryEntry handles DELETE /v1/user/memories/:memory/entries/:id.
func (h *Handler) DeleteMemoryEntry(c *gin.Context) { h.deleteMemoryEntry(c, true) }

// AdminDeleteMemoryEntry handles DELETE /v1/admin/agent/memories/:memory/entries/:id.
func (h *Handler) AdminDeleteMemoryEntry(c *gin.Context) { h.deleteMemoryEntry(c, false) }

// SignedDeleteMemoryEntry handles DELETE /v1/agent/memories/:memory/entries/:id.
func (h *Handler) SignedDeleteMemoryEntry(c *gin.Context) { h.deleteMemoryEntry(c, false) }

func (h *Handler) deleteMemoryEntry(c *gin.Context, forceSubject bool) {
	core.HandleRequest(c, restbind.URIWith(c, func(rq *v1.DeleteMemoryEntryRequest) {
		rq.Subject = entrySubject(c, forceSubject)
	}), h.biz.MemoryV1().DeleteEntry, h.val.ValidateDeleteMemoryEntryRequest)
}

// BatchDeleteMemoryEntries handles POST /v1/user/memories/:memory/entries/batch-delete.
func (h *Handler) BatchDeleteMemoryEntries(c *gin.Context) { h.batchDelete(c, true) }

// AdminBatchDeleteMemoryEntries handles POST /v1/admin/agent/memories/:memory/entries/batch-delete.
func (h *Handler) AdminBatchDeleteMemoryEntries(c *gin.Context) { h.batchDelete(c, false) }

// SignedBatchDeleteMemoryEntries handles POST /v1/agent/memories/:memory/entries/batch-delete.
func (h *Handler) SignedBatchDeleteMemoryEntries(c *gin.Context) { h.batchDelete(c, false) }

func (h *Handler) batchDelete(c *gin.Context, forceSubject bool) {
	core.HandleRequest(c, restbind.JSONWithPath(c, func(rq *v1.BatchDeleteMemoryEntriesRequest) {
		rq.Memory = c.Param("memory")
		if forceSubject {
			rq.Subject = claimsSubject(c)
		} else if rq.Subject == "" {
			rq.Subject = c.Query("subject")
		}
	}), h.biz.MemoryV1().BatchDeleteEntries, h.val.ValidateBatchDeleteMemoryEntriesRequest)
}

// SearchMemoryEntries handles POST /v1/user/memories/:memory/entries/search.
func (h *Handler) SearchMemoryEntries(c *gin.Context) { h.searchEntries(c, true) }

// AdminSearchMemoryEntries handles POST /v1/admin/agent/memories/:memory/entries/search.
func (h *Handler) AdminSearchMemoryEntries(c *gin.Context) { h.searchEntries(c, false) }

// SignedSearchMemoryEntries handles POST /v1/agent/memories/:memory/entries/search.
func (h *Handler) SignedSearchMemoryEntries(c *gin.Context) { h.searchEntries(c, false) }

func (h *Handler) searchEntries(c *gin.Context, forceSubject bool) {
	withAdminCaller(c, forceSubject)
	core.HandleRequest(c, restbind.JSONWithPath(c, func(rq *v1.SearchMemoryEntriesRequest) {
		rq.Memory = c.Param("memory")
		if forceSubject {
			rq.Subject = claimsSubject(c)
		} else if rq.Subject == "" {
			rq.Subject = c.Query("subject")
		}
	}), h.biz.MemoryV1().SearchEntries, h.val.ValidateSearchMemoryEntriesRequest)
}

// GetMemoryStats handles GET /v1/user/memories/:memory/stats.
func (h *Handler) GetMemoryStats(c *gin.Context) { h.memoryStats(c, true) }

// AdminGetMemoryStats handles GET /v1/admin/agent/memories/:memory/stats.
func (h *Handler) AdminGetMemoryStats(c *gin.Context) { h.memoryStats(c, false) }

func (h *Handler) memoryStats(c *gin.Context, forceSubject bool) {
	core.HandleRequest(c, queryWith(c, func(rq *v1.GetMemoryStatsRequest) {
		rq.Memory = c.Param("memory")
		if forceSubject {
			rq.Subject = claimsSubject(c)
		} else if rq.Subject == "" {
			rq.Subject = c.Query("subject")
		}
	}), h.biz.MemoryV1().Stats, h.val.ValidateGetMemoryStatsRequest)
}

// AdminListMemorySubjects handles GET /v1/admin/agent/memories/:memory/subjects.
func (h *Handler) AdminListMemorySubjects(c *gin.Context) { h.listSubjects(c, false) }

// SignedListMemorySubjects handles GET /v1/agent/memories/:memory/subjects.
func (h *Handler) SignedListMemorySubjects(c *gin.Context) { h.listSubjects(c, false) }

func (h *Handler) listSubjects(c *gin.Context, forceSubject bool) {
	_ = forceSubject
	core.HandleRequest(c, queryWith(c, func(rq *v1.ListMemorySubjectsRequest) {
		rq.Memory = c.Param("memory")
	}), h.biz.MemoryV1().Subjects, h.val.ValidateListMemorySubjectsRequest)
}

// ListMemoryAudit handles GET /v1/admin/agent/memories/:memory/audit.
func (h *Handler) ListMemoryAudit(c *gin.Context) {
	core.HandleRequest(c, queryWith(c, func(rq *v1.ListMemoryAuditRequest) {
		rq.Memory = c.Param("memory")
	}), h.biz.MemoryV1().ListAudit, h.val.ValidateListMemoryAuditRequest)
}

// ReembedMemory handles POST /v1/admin/agent/memories/:memory/reembed.
func (h *Handler) ReembedMemory(c *gin.Context) {
	core.HandleRequest(c, restbind.JSONWithPathAllowEmpty(c, func(rq *v1.ReembedMemoryRequest) {
		rq.Memory = c.Param("memory")
	}), h.biz.MemoryV1().Reembed, h.val.ValidateReembedMemoryRequest)
}

// EvalMemory handles POST /v1/admin/agent/memories/:memory/eval.
func (h *Handler) EvalMemory(c *gin.Context) {
	core.HandleAllRequest(c, h.biz.MemoryV1().Eval, h.val.ValidateEvalMemoryRequest)
}

// PurgeMemorySubject handles DELETE /v1/admin/agent/memories/:memory/subjects/:subject.
func (h *Handler) PurgeMemorySubject(c *gin.Context) {
	core.HandleRequest(c, restbind.URIWith(c, func(rq *v1.PurgeMemorySubjectRequest) {
		rq.Actor = claimsSubject(c)
	}), h.biz.MemoryV1().PurgeSubject, h.val.ValidatePurgeMemorySubjectRequest)
}

// withAdminCaller records the browser user on the request context so the biz
// layer can apply the Memory's allowAdminContentRead policy. Self-service calls
// pass forceSubject=true and are never redacted: the subject is the caller.
func withAdminCaller(c *gin.Context, forceSubject bool) {
	if forceSubject {
		return
	}
	if caller := claimsSubject(c); caller != "" {
		c.Request = c.Request.WithContext(memorysvc.WithCaller(c.Request.Context(), caller))
	}
}

func claimsSubject(c *gin.Context) string {
	claims, ok := bearer.ClaimsFrom(c)
	if !ok {
		return ""
	}
	return claims.UserID
}

func entrySubject(c *gin.Context, force bool) string {
	if force {
		return claimsSubject(c)
	}
	if subject := c.Query("subject"); subject != "" {
		return subject
	}
	return ""
}

func queryWith[T any](c *gin.Context, mutate func(*T)) core.Binder {
	return func(obj any) error {
		rq, ok := obj.(*T)
		if !ok {
			return errno.ErrInternal.WithMessage("memory: unexpected request type %T", obj)
		}
		if err := c.ShouldBindQuery(rq); err != nil {
			return errno.ErrBind.WithMessage("%s", err.Error())
		}
		mutate(rq)
		return nil
	}
}

func init() { //nolint:gochecknoinits
	RegisterUnified(func(engine *gin.RouterGroup, h *Handler, _ ...gin.HandlerFunc) {
		g := engine.Group("/agent/memories")
		g.POST("", h.CreateMemory)
		g.GET("", h.ListMemory)
		g.GET("/:memory", h.GetMemory)
		g.PUT("/:memory", h.UpdateMemory)
		g.DELETE("/:memory", h.DeleteMemory)
	})

	RegisterSelfService(func(engine *gin.RouterGroup, h *Handler, _ ...gin.HandlerFunc) {
		g := engine.Group("/user/memories/:memory")
		g.POST("/entries", h.CreateMemoryEntry)
		g.GET("/entries", h.ListMemoryEntries)
		g.POST("/entries/batch-delete", h.BatchDeleteMemoryEntries)
		g.POST("/entries/search", h.SearchMemoryEntries)
		g.GET("/entries/:id", h.GetMemoryEntry)
		g.PATCH("/entries/:id", h.UpdateMemoryEntry)
		g.DELETE("/entries/:id", h.DeleteMemoryEntry)
		g.GET("/stats", h.GetMemoryStats)
	})

	RegisterAdmin(func(engine *gin.RouterGroup, h *Handler, _ ...gin.HandlerFunc) {
		g := engine.Group("/agent/memories/:memory")
		g.POST("/entries", h.AdminCreateMemoryEntry)
		g.GET("/entries", h.AdminListMemoryEntries)
		g.POST("/entries/batch-delete", h.AdminBatchDeleteMemoryEntries)
		g.POST("/entries/search", h.AdminSearchMemoryEntries)
		g.GET("/entries/:id", h.AdminGetMemoryEntry)
		g.PATCH("/entries/:id", h.AdminUpdateMemoryEntry)
		g.DELETE("/entries/:id", h.AdminDeleteMemoryEntry)
		g.GET("/stats", h.AdminGetMemoryStats)
		g.GET("/subjects", h.AdminListMemorySubjects)
		g.GET("/audit", h.ListMemoryAudit)
		g.POST("/reembed", h.ReembedMemory)
		g.POST("/eval", h.EvalMemory)
		g.DELETE("/subjects/:subject", h.PurgeMemorySubject)
	})

	Register(func(engine *gin.RouterGroup, h *Handler, _ ...gin.HandlerFunc) {
		g := engine.Group("/agent/memories/:memory")
		g.POST("/entries", h.SignedCreateMemoryEntry)
		g.GET("/entries", h.SignedListMemoryEntries)
		g.POST("/entries/batch-delete", h.SignedBatchDeleteMemoryEntries)
		g.POST("/entries/search", h.SignedSearchMemoryEntries)
		g.GET("/entries/:id", h.SignedGetMemoryEntry)
		g.PATCH("/entries/:id", h.SignedUpdateMemoryEntry)
		g.DELETE("/entries/:id", h.SignedDeleteMemoryEntry)
		g.GET("/stats", h.AdminGetMemoryStats)
		g.GET("/subjects", h.SignedListMemorySubjects)
		g.GET("/audit", h.ListMemoryAudit)
		g.POST("/reembed", h.ReembedMemory)
		g.POST("/eval", h.EvalMemory)
		g.DELETE("/subjects/:subject", h.PurgeMemorySubject)
	})
}
