// Package auth implements the credential primitives of the panel: HS384 JWT
// signing/verification with express-jwt compatible error messages, scrypt
// password hashing that is byte-compatible with back/shared/password.ts, TOTP,
// the per-IP login throttle, the authInfo document and its token lists.
package auth
