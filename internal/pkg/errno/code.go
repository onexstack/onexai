package errno

import (
	"net/http"

	"github.com/onexstack/onexstack/pkg/errorsx"
)

// Global application-specific error definitions.
// These errors are intended to be returned when specific conditions occur.

// OK represents a successful request.
var OK = errorsx.New(http.StatusOK, "", "")

// ErrInternal represents all unknown server-side errors.
// This error is typically used for unexpected issues within the service.
var ErrInternal = errorsx.ErrInternal // Assuming errorsx provides this common error

// ErrNotFound indicates that the requested resource was not found.
var ErrNotFound = errorsx.ErrNotFound // Assuming errorsx provides this common error

// ErrBind indicates an error occurred while binding the request body.
// This typically signifies malformed input from the client.
var ErrBind = errorsx.ErrBind // Assuming errorsx provides this common error

// ErrInvalidArgument indicates that an argument provided in the request
// failed validation.
var ErrInvalidArgument = errorsx.ErrInvalidArgument // Assuming errorsx provides this common error

// ErrUnauthenticated indicates authentication failure.
// This error is returned when the client lacks valid authentication credentials.
var ErrUnauthenticated = errorsx.ErrUnauthenticated // Assuming errorsx provides this common error

// ErrPermissionDenied indicates that the request was forbidden due to
// insufficient permissions for the authenticated user.
var ErrPermissionDenied = errorsx.ErrPermissionDenied // Assuming errorsx provides this common error

// ErrAccountDisabled reports that the signed-in account has been disabled by an
// administrator. It is a distinct code because the console renders a different
// message for it ("请联系管理员") than for an ordinary permission refusal.
var ErrAccountDisabled = errorsx.New(
	http.StatusForbidden, "Forbidden.AccountDisabled", "The account has been disabled.")

// ErrOperationFailed indicates that a generic operation failed for an
// unspecified reason. More specific errors should be used when possible.
var ErrOperationFailed = errorsx.ErrOperationFailed

// ErrAlreadyExists reports a uniqueness conflict (a username or a Feishu
// identity that is already taken). 409 rather than 400: the request is
// well-formed, it just cannot be applied to the current state.
var ErrAlreadyExists = errorsx.New(http.StatusConflict, "AlreadyExists", "The resource already exists.") // Assuming errorsx provides this common error

// ErrConflict reports a write that lost a race: the object changed since the
// caller read it, so applying its copy would overwrite somebody else's edit.
// It is separate from ErrAlreadyExists because the caller's move differs —
// re-read and re-apply, rather than pick another name.
var ErrConflict = errorsx.New(http.StatusConflict, "Conflict", "The resource was modified since it was read.")

// ErrRunNotRetryable reports that an async run cannot be replayed in its
// current state (it is still active, or it already succeeded). It is 409
// rather than 400 because the request is well-formed; the state is the reason.
var ErrRunNotRetryable = errorsx.New(http.StatusConflict, "FailedPrecondition.RunNotRetryable", "The run cannot be retried in its current state.")

// ErrPageNotFound indicates that the specific page or route was not found.
var ErrPageNotFound = errorsx.New(http.StatusNotFound, "NotFound.PageNotFound", "The requested page was not found.")

// ErrTooManyRequests reports that a caller has used their allowance. It is a
// refusal with a time on it, and 429 says exactly that — a caller can tell
// "slow down" from "you may not" without reading the message.
var ErrTooManyRequests = errorsx.New(http.StatusTooManyRequests, "TooManyRequests.QuotaExceeded", "The request quota has been reached.")

// ErrPayloadTooLarge refuses a request whose body exceeds the endpoint's
// limit. It is separate from ErrInvalidArgument because the caller can act on
// the distinction: a truncated or malformed body is a bug, while an oversized
// one is a size the endpoint will never accept.
var ErrPayloadTooLarge = errorsx.New(http.StatusRequestEntityTooLarge, "RequestEntityTooLarge", "The request body is too large.")

// ErrNotConfigured reports that the deployment has not been given what the
// request needs — no media driver to store an image in, no image model to draw
// with.
//
// It is a distinct code rather than a generic internal error because the fact
// is on the wire, not only in the message: a caller that wants to tell "this
// deployment does not do images" from "the image provider failed" can read the
// code. Collapsing the two into ErrInternal forces every consumer to match the
// message text, which is the one thing a wire contract should never require.
//
// 503 rather than 500 because nothing is broken: the capability is absent until
// an operator supplies the configuration, which is what Service Unavailable
// says.
var ErrNotConfigured = errorsx.New(http.StatusServiceUnavailable, "ServiceUnavailable.NotConfigured", "The requested capability is not configured in this deployment.")

// ErrApprovalNotPending indicates that an approval answered a pause the run is
// no longer waiting on: already answered, expired, or naming an earlier pause of
// the same run.
//
// It is Gone rather than Conflict so a client can tell it from a conversation
// that is merely busy. The two call for different words — one means ask the run
// again, the other means try again shortly — and a client that could not
// distinguish them would have to say "something went wrong" to both.
var ErrApprovalNotPending = errorsx.New(http.StatusGone, "Gone.ApprovalNotPending", "The approval is no longer pending.")

// ErrIdempotencyConflict indicates that the request carried an idempotency key
// that is still claimed: an identical request is in flight, or already ran.
//
// Conflict rather than Too Many Requests because the caller did nothing wrong —
// it is the same request arriving twice, which is what a retry after a lost
// response looks like. The code tells it the work is already owned by someone.
var ErrIdempotencyConflict = errorsx.New(http.StatusConflict, "Conflict.IdempotentRequestInFlight", "A request with this idempotency key is already in flight or has already been processed.")

// ErrIdempotencyStoreUnavailable indicates that the idempotency key could not be
// claimed, so the request was refused rather than executed unchecked.
var ErrIdempotencyStoreUnavailable = errorsx.New(http.StatusServiceUnavailable, "ServiceUnavailable.IdempotencyStoreUnavailable", "The idempotency key could not be recorded, so the request was not executed.")

// ErrSignToken indicates an error occurred during the process of signing
// a JSON Web Token (JWT).
var ErrSignToken = errorsx.New(http.StatusUnauthorized, "Unauthenticated.SignToken", "Error occurred while signing the JSON web token.")

// ErrTokenInvalid indicates that the provided JWT token was invalid,
// either due to malformation, expiration, or incorrect signature.
var ErrTokenInvalid = errorsx.New(http.StatusUnauthorized, "Unauthenticated.TokenInvalid", "The provided token was invalid.")

// ErrDBRead indicates a failure during a database read operation.
var ErrDBRead = errorsx.New(http.StatusInternalServerError, "InternalError.DBRead", "A database read operation failed.")

// ErrDBWrite indicates a failure during a database write operation.
var ErrDBWrite = errorsx.New(http.StatusInternalServerError, "InternalError.DBWrite", "A database write operation failed.")

// ErrAddRole indicates an error occurred while adding a user role.
var ErrAddRole = errorsx.New(http.StatusInternalServerError, "InternalError.AddRole", "Error occurred while adding the role.")

// ErrRemoveRole indicates an error occurred while removing a user role.
var ErrRemoveRole = errorsx.New(http.StatusInternalServerError, "InternalError.RemoveRole", "Error occurred while removing the role.")
