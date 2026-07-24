package httpapi // HTTP 接口层。

import (
	"context"  // 在请求上下文中保存认证身份。
	"errors"   // 判断具体认证错误类型。
	"net/http" // 读取请求头并使用 HTTP 状态码。
	"strings"  // 解析 Authorization 请求头。

	"github.com/gin-gonic/gin" // Gin 中间件与请求上下文。

	"model-velo/internal/apikey"    // API Key 身份和认证错误。
	"model-velo/internal/ratelimit" // 租户限流结果。
)

const identityGinKey = "model-velo.identity" // 身份在 Gin Context 中的存储键。

type identityContextKey struct{} // 身份在标准 context.Context 中使用的专用键类型。

type AccessController interface { // 认证与模型授权服务接口。
	Authenticate(ctx context.Context, plaintext string) (apikey.Identity, error) // 验证网关 API Key 并返回租户身份。
	AuthorizeModel(ctx context.Context, tenantID, model string) error            // 检查租户是否允许使用指定模型。
}

type RateLimiter interface { // 租户限流服务接口。
	Allow(ctx context.Context, tenantID, model string) (ratelimit.Decision, error) // 判断当前租户是否还能请求该模型。
}

func authenticationMiddleware(access AccessController) gin.HandlerFunc { // 创建使用 access 的认证中间件。
	return func(c *gin.Context) { // 每个进入受保护路由的请求都会执行该函数。
		plaintext, err := bearerToken(c.Request) // 从 Authorization 头提取明文网关 API Key。
		if err != nil {                          // 请求头缺失或格式错误。
			c.Header("WWW-Authenticate", "Bearer") // 告诉客户端应使用 Bearer 认证。
			writeAPIError(
				c,
				http.StatusUnauthorized, // 返回 401。
				err.message,             // 返回具体请求头错误信息。
				"authentication_error",  // 错误类型。
				nil,                     // 没有具体请求参数。
				err.code,                // 具体错误码。
			)
			return // 不再进入后续路由。
		}

		identity, authenticateErr := access.Authenticate(c.Request.Context(), plaintext) // 验证 API Key 并取得租户身份。
		if authenticateErr != nil {                                                      // API Key 验证失败。
			if c.Request.Context().Err() != nil { // 请求已经取消时不再写响应。
				return
			}
			if errors.Is(authenticateErr, apikey.ErrInvalidCredential) || // API Key 无效。
				errors.Is(authenticateErr, apikey.ErrKeyInactive) || // API Key 未启用。
				errors.Is(authenticateErr, apikey.ErrKeyRevoked) || // API Key 已吊销。
				errors.Is(authenticateErr, apikey.ErrKeyExpired) || // API Key 已过期。
				errors.Is(authenticateErr, apikey.ErrTenantInactive) { // 所属租户未启用。
				c.Header("WWW-Authenticate", "Bearer") // 要求客户端重新提供 Bearer API Key。
				writeAPIError(
					c,
					http.StatusUnauthorized, // 认证失败返回 401。
					"API key is invalid or inactive",
					"authentication_error",
					nil,
					"invalid_api_key",
				)
				return
			}

			writeAPIError(
				c,
				http.StatusServiceUnavailable, // 认证服务异常返回 503。
				"authentication service is unavailable",
				"server_error",
				nil,
				"authentication_unavailable",
			)
			return
		}

		c.Set(identityGinKey, identity)    // 将身份保存到 Gin Context。
		c.Request = c.Request.WithContext( // 为请求替换包含身份的新 Context。
			context.WithValue(c.Request.Context(), identityContextKey{}, identity), // 将身份保存到标准 Context。
		)
		c.Next() // 认证成功，继续执行后续中间件和接口函数。
	}
}

type bearerTokenError struct { // Authorization 请求头解析错误。
	message string // 返回给客户端的错误信息。
	code    string // 对外错误码。
}

func bearerToken(request *http.Request) (string, *bearerTokenError) { // 从请求中解析 Bearer API Key。
	values := request.Header.Values("Authorization") // 取得全部 Authorization 请求头。
	if len(values) == 0 {                            // 没有 Authorization 请求头。
		return "", &bearerTokenError{
			message: "Authorization header with Bearer API key is required", // 提示必须提供 Bearer API Key。
			code:    "missing_api_key",                                      // 缺少 API Key。
		}
	}
	if len(values) != 1 { // 只允许一个 Authorization 请求头。
		return "", &bearerTokenError{
			message: "Authorization header is invalid",
			code:    "invalid_authorization",
		}
	}

	value := values[0]                     // 取得唯一的 Authorization 值。
	if value != strings.TrimSpace(value) { // 首尾存在多余空格时视为非法。
		return "", &bearerTokenError{
			message: "Authorization header is invalid",
			code:    "invalid_authorization",
		}
	}

	scheme, plaintext, found := strings.Cut(value, " ") // 按第一个空格拆成认证方式和 API Key。
	if !found ||                                        // 没有空格，无法拆分。
		!strings.EqualFold(scheme, "Bearer") || // 认证方式必须是 Bearer，忽略大小写。
		plaintext == "" || // API Key 不能为空。
		strings.ContainsAny(plaintext, " \t\r\n") { // API Key 内不能再包含空白字符。
		return "", &bearerTokenError{
			message: "Authorization header must use Bearer authentication",
			code:    "invalid_authorization",
		}
	}

	return plaintext, nil // 返回去掉 Bearer 前缀后的明文 API Key。
}

func identityFromContext(ctx context.Context) (apikey.Identity, bool) { // 从标准 Context 中读取认证身份。
	identity, ok := ctx.Value(identityContextKey{}).(apikey.Identity) // 读取并断言为 apikey.Identity。
	return identity, ok                                               // 返回身份以及是否成功取得。
}
