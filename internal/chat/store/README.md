# Store layer

Store code changes less often than the layers above it, so it has one version
rather than a `v1`/`v2` tree.

## Rules

- Every database table has one Store resource, and its CRUD code lives in its
  own file.
- Every Store resource has the standard five methods plus an expansion
  interface:

```go
// PostStore is the store-layer surface for the post resource.
type PostStore interface {
    Create(ctx context.Context, obj *model.PostM) error
    Update(ctx context.Context, obj *model.PostM) error
    Delete(ctx context.Context, opts *where.Options) error
    Get(ctx context.Context, opts *where.Options) (*model.PostM, error)
    List(ctx context.Context, opts *where.Options) (int64, []*model.PostM, error)

    PostExpansion
}

// PostExpansion holds the post operations that are not the standard five.
type PostExpansion interface{}
```

- The standard methods inherit from `genericstore`; override a method only when
  the resource needs different behaviour.
- A store decorator (for example the ticket→incident mirror) implements the
  same interface and wraps the base store; business logic must not need to know
  which backend answered.
