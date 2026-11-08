package httpapi

import (
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"qinglong-go/internal/auth"
	"qinglong-go/internal/i18n"
	"qinglong-go/internal/service"
)

// credentialRules are shared by login, init and account updates.
var credentialRules = []fieldRule{
	{name: "username", kind: kindString, required: true, maxLen: 1024},
	{name: "password", kind: kindString, required: true, maxLen: 1024},
}

// imageTypes is the avatar whitelist from back/api/user.ts.
var imageTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".avif": "image/avif",
}

// registerUserRoutes mounts the 17 routes of back/api/user.ts under /api/user.
func registerUserRoutes(router chi.Router, deps Deps) {
	loginLimiter := NewRateLimiter(100, 15*time.Minute, deps.Config.TrustProxy)
	// Node declares a separate rateLimit() instance per route, so init and the
	// 2FA login each own a 20/15min counter instead of sharing one.
	initLimiter := NewRateLimiter(20, 15*time.Minute, deps.Config.TrustProxy)
	twoFactorLimiter := NewRateLimiter(20, 15*time.Minute, deps.Config.TrustProxy)

	router.With(loginLimiter.Handler).Post("/api/user/login", func(w http.ResponseWriter, r *http.Request) {
		body, ok := validateBody(w, r, credentialRules)
		if !ok {
			return
		}
		reply, err := deps.User.Login(r.Context(),
			body["username"].(string), body["password"].(string),
			auth.ClientIP(r, deps.Config.TrustProxy), platformFrom(r.Context()))
		respond(w, reply, err)
	})

	router.Post("/api/user/logout", func(w http.ResponseWriter, r *http.Request) {
		token := auth.TokenFromHeader(r.Header.Get("Authorization"))
		reply, err := deps.User.Logout(r.Context(), platformFrom(r.Context()), token)
		respond(w, reply, err)
	})

	router.Put("/api/user", func(w http.ResponseWriter, r *http.Request) {
		body, ok := validateBody(w, r, credentialRules)
		if !ok {
			return
		}
		if deps.Config.IsDemo() {
			writeReply(w, &service.Reply{Code: 450, Message: i18n.T("未知错误")})
			return
		}
		reply, err := deps.User.UpdateUser(r.Context(), body["username"].(string), body["password"].(string))
		respond(w, reply, err)
	})

	router.Get("/api/user", func(w http.ResponseWriter, r *http.Request) {
		data, err := deps.User.UserInfo(r.Context())
		if err != nil {
			respond(w, nil, err)
			return
		}
		writeReply(w, &service.Reply{Code: 200, Data: data})
	})

	router.Get("/api/user/two-factor/init", func(w http.ResponseWriter, r *http.Request) {
		reply, err := deps.User.TwoFactorInit(r.Context())
		respond(w, reply, err)
	})

	router.Put("/api/user/two-factor/active", func(w http.ResponseWriter, r *http.Request) {
		body, ok := validateBody(w, r, []fieldRule{{name: "code", kind: kindString, required: true}})
		if !ok {
			return
		}
		reply, err := deps.User.TwoFactorActive(r.Context(), body["code"].(string))
		respond(w, reply, err)
	})

	router.Put("/api/user/two-factor/deactivate", func(w http.ResponseWriter, r *http.Request) {
		reply, err := deps.User.TwoFactorDeactivate(r.Context())
		respond(w, reply, err)
	})

	router.With(twoFactorLimiter.Handler).Put("/api/user/two-factor/login", func(w http.ResponseWriter, r *http.Request) {
		body, ok := validateBody(w, r, []fieldRule{
			{name: "code", kind: kindString, required: true},
			{name: "username", kind: kindString, required: true, maxLen: 1024},
			{name: "password", kind: kindString, required: true, maxLen: 1024},
		})
		if !ok {
			return
		}
		reply, err := deps.User.TwoFactorLogin(r.Context(),
			body["username"].(string), body["password"].(string), body["code"].(string),
			auth.ClientIP(r, deps.Config.TrustProxy), platformFrom(r.Context()))
		respond(w, reply, err)
	})

	router.Get("/api/user/login-log", func(w http.ResponseWriter, r *http.Request) {
		data, err := deps.User.LoginLog(r.Context())
		if err != nil {
			respond(w, nil, err)
			return
		}
		writeReply(w, &service.Reply{Code: 200, Data: data})
	})

	router.Get("/api/user/ip-blacklist", func(w http.ResponseWriter, r *http.Request) {
		data, err := deps.User.IPBlacklist(r.Context())
		if err != nil {
			respond(w, nil, err)
			return
		}
		writeReply(w, &service.Reply{Code: 200, Data: data})
	})

	ipRule := []fieldRule{{name: "ip", kind: kindIP, required: true}}
	router.Put("/api/user/ip-blacklist", func(w http.ResponseWriter, r *http.Request) {
		body, ok := validateBody(w, r, ipRule)
		if !ok {
			return
		}
		data, err := deps.User.BlockIP(r.Context(), body["ip"].(string))
		if err != nil {
			respond(w, nil, err)
			return
		}
		writeReply(w, &service.Reply{Code: 200, Data: data, Message: i18n.T("已加入 IP 黑名单")})
	})

	router.Delete("/api/user/ip-blacklist", func(w http.ResponseWriter, r *http.Request) {
		body, ok := validateBody(w, r, ipRule)
		if !ok {
			return
		}
		data, err := deps.User.UnblockIP(r.Context(), body["ip"].(string))
		if err != nil {
			respond(w, nil, err)
			return
		}
		writeReply(w, &service.Reply{Code: 200, Data: data, Message: i18n.T("已移出 IP 黑名单")})
	})

	router.Get("/api/user/notification", func(w http.ResponseWriter, r *http.Request) {
		data, err := deps.User.Notification(r.Context())
		if err != nil {
			respond(w, nil, err)
			return
		}
		writeReply(w, &service.Reply{Code: 200, Data: data})
	})

	updateNotification := func(w http.ResponseWriter, r *http.Request) {
		payload, err := readJSONBody(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, envelope{Code: 400, Message: "Unexpected token in JSON"})
			return
		}
		reply := deps.Notify.UpdateConfig(r.Context(), payload)
		respond(w, reply, nil)
	}
	router.Put("/api/user/notification", updateNotification)
	router.Put("/api/user/notification/init", updateNotification)

	router.With(initLimiter.Handler).Put("/api/user/init", func(w http.ResponseWriter, r *http.Request) {
		body, ok := validateBody(w, r, credentialRules)
		if !ok {
			return
		}
		reply, err := deps.User.Init(r.Context(), body["username"].(string), body["password"].(string))
		respond(w, reply, err)
	})

	router.Put("/api/user/avatar", func(w http.ResponseWriter, r *http.Request) {
		handleAvatarUpload(deps, w, r)
	})
}

// handleAvatarUpload reproduces multer.diskStorage + the image filter.
func handleAvatarUpload(deps Deps, w http.ResponseWriter, r *http.Request) {
	const maxAvatarBytes = 5 << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxAvatarBytes+(1<<20))
	if err := r.ParseMultipartForm(maxAvatarBytes); err != nil {
		message := err.Error()
		if strings.Contains(strings.ToLower(message), "too large") {
			message = "File too large"
		}
		writeError(w, http.StatusInternalServerError, message)
		return
	}
	file, header, err := r.FormFile("avatar")
	if err != nil {
		// Node dereferences req.file!.filename and throws this TypeError.
		writeError(w, http.StatusInternalServerError, "Cannot read properties of undefined (reading 'filename')")
		return
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(header.Filename))
	expected, allowed := imageTypes[ext]
	if !allowed || expected != header.Header.Get("Content-Type") {
		writeError(w, http.StatusInternalServerError, i18n.T("仅支持 PNG、JPEG、GIF、WebP、AVIF 图片"))
		return
	}

	filename := uuidV4() + ext
	if err := os.MkdirAll(deps.Config.UploadPath, 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out, err := os.Create(filepath.Join(deps.Config.UploadPath, filename))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer out.Close()
	if _, err := io.Copy(out, file); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	reply, err := deps.User.UpdateAvatar(r.Context(), filename)
	respond(w, reply, err)
}

// respond turns a service result into the panel envelope. Business codes are
// always HTTP 200; only an unexpected error becomes a 500.
func respond(w http.ResponseWriter, reply *service.Reply, err error) {
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeReply(w, reply)
}

func writeReply(w http.ResponseWriter, reply *service.Reply) {
	if reply == nil {
		writeJSON(w, http.StatusOK, envelope{Code: 200})
		return
	}
	body := map[string]any{"code": reply.Code}
	if reply.Data != nil {
		body["data"] = reply.Data
	}
	if reply.Message != "" || reply.MessageSet {
		body["message"] = reply.Message
	}
	writeJSON(w, http.StatusOK, body)
}

// uuidV4 renders a random RFC 4122 version 4 UUID, matching uuid.v4().
func uuidV4() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}
