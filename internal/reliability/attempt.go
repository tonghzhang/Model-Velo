// reliability 包负责熔断、排队、Key 选择、重试和错误分类等可靠性控制。
package reliability

import (
	// context 用于接收请求级上下文。
	//
	// 当客户端断开、总请求超时或上层主动取消时，
	// 可以停止排队、重试等待和上游请求。
	"context"

	// errors 用于创建错误以及通过 errors.Is 判断具体错误类型。
	"errors"

	// time 用于记录单次上游请求耗时。
	"time"

	// provider 包提供 Provider Adapter、聊天请求结构和上游错误。
	"model-velo/internal/provider"

	// routing 包提供路由产生的候选 Provider 信息。
	"model-velo/internal/routing"
)

// AttemptInput 是执行一个候选 Provider 时需要的全部输入。
//
// 上层 Orchestrator 遍历路由候选时，
// 会为当前 Candidate 组装一个 AttemptInput，
// 然后交给 AttemptExecutor.Execute。
type AttemptInput struct {
	// RequestID 是当前网关请求的唯一标识。
	//
	// 它通常来自 HTTP 请求上下文，
	// 后续会继续传给 Provider Adapter，方便日志追踪。
	RequestID string

	// RequestedModel 是客户端原始请求的模型名。
	//
	// 例如客户端请求的是虚拟模型：
	// company-default
	//
	// 后续会与 Candidate.UpstreamModel 比较，
	// 判断是否需要覆盖发送给上游的真实模型名。
	RequestedModel string

	// Request 是已经解析完成的统一聊天请求。
	//
	// 里面包含 messages、stream、temperature 等请求内容，
	// 后续会交给当前 Provider Adapter。
	Request provider.ChatRequest

	// Candidate 是路由系统为当前请求选出的一个候选 Provider。
	//
	// 其中包含：
	// ProviderID：调用哪家 Provider；
	// UpstreamModel：实际调用哪个上游模型；
	// Priority：这是第几个候选。
	Candidate routing.Candidate
}

// AttemptResult 是在一个候选 Provider 内执行后的结果。
//
// Execute 可能在同一个 Provider 内重试多次，
// 所以这里既保存最终响应，也保存所有尝试记录。
type AttemptResult struct {
	// Body 是上游成功返回的原始响应体。
	//
	// 只有最终请求成功时才会被赋值。
	Body []byte

	// ProviderID 是本次候选对应的 Provider ID。
	ProviderID string

	// UpstreamModel 是本次实际准备调用的上游模型。
	UpstreamModel string

	// KeyID 是最后一次选中的 Provider API Key ID。
	//
	// 注意这里只记录 Key 的标识，不记录真正的 Secret。
	KeyID string

	// Attempts 是实际向上游发出过多少次请求。
	//
	// 某些错误可能发生在调用上游之前，
	// 例如没有 Adapter、熔断拒绝、排队失败，
	// 这种情况不一定计入 Attempts。
	Attempts int

	// Trail 保存每一次真实上游调用的执行记录。
	//
	// 可以用于日志、监控和排查重试过程。
	Trail []AttemptRecord
}

// AttemptRecord 描述一次实际执行过的上游调用。
type AttemptRecord struct {
	// ProviderID 是本次调用的 Provider。
	ProviderID string

	// UpstreamModel 是本次调用的真实上游模型。
	UpstreamModel string

	// KeyID 是本次调用所使用的 Provider Key ID。
	KeyID string

	// Candidate 表示当前 Provider 在路由候选列表中的优先级。
	Candidate int

	// Attempt 表示当前 Provider 内第几次真实调用上游。
	Attempt int

	// Duration 是本次 Adapter.Complete 调用耗费的时间。
	Duration time.Duration

	// Category 是本次调用结束后的错误分类。
	//
	// 成功时为空字符串。
	Category Category

	// StatusCode 是上游返回的 HTTP 状态码。
	//
	// 成功或者没有 HTTP 状态码时为 0。
	StatusCode int
}

// AttemptExecutor 负责在一个候选 Provider 内执行完整调用流程。
//
// 它不负责遍历不同 Provider，
// 只负责当前 Candidate 内的：
//
// 熔断检查
//
//	↓
//
// 排队获取并发位置
//
//	↓
//
// 选择 Provider Key
//
//	↓
//
// 调用 Provider Adapter
//
//	↓
//
// 反馈 Key 和熔断器状态
//
//	↓
//
// 判断是否重试
type AttemptExecutor struct {
	// adapters 保存所有 Provider 对应的 Adapter。
	//
	// Execute 根据 Candidate.ProviderID 从中找到具体 Adapter，
	// 再由 Adapter 调用真实上游接口。
	adapters *provider.AdapterRegistry

	// breakers 保存每个 Provider 对应的熔断器。
	//
	// 每次调用前检查 Provider 是否允许请求，
	// 调用结束后再反馈成功或失败。
	breakers *BreakerRegistry

	// queues 保存每个 Provider 对应的并发队列。
	//
	// 每次调用上游前必须先取得一个并发位置。
	queues *QueueRegistry

	// keys 保存每个 Provider 可使用的 API Key。
	//
	// 只有需要 API Key 鉴权的 Adapter 才会使用。
	keys *ProviderKeyRegistry

	// retries 保存每个 Provider 对应的重试策略。
	//
	// 用于决定最大重试次数、退避时间和单次请求超时。
	retries RetryPolicies
}

// NewAttemptExecutor 创建 AttemptExecutor。
//
// 这些依赖通常在应用启动阶段创建，
// 然后统一注入 AttemptExecutor。
func NewAttemptExecutor(
	adapters *provider.AdapterRegistry,
	breakers *BreakerRegistry,
	queues *QueueRegistry,
	keys *ProviderKeyRegistry,
	retries RetryPolicies,
) (*AttemptExecutor, error) {
	// Adapter 注册表不能为空，
	// 否则无法找到和调用具体 Provider。
	if adapters == nil {
		return nil, errors.New(
			"attempt executor requires a provider adapter registry",
		)
	}

	// 熔断器注册表不能为空，
	// 否则无法控制故障 Provider 的调用。
	if breakers == nil {
		return nil, errors.New(
			"attempt executor requires a circuit breaker registry",
		)
	}

	// 队列注册表不能为空，
	// 否则无法限制每个 Provider 的并发请求数。
	if queues == nil {
		return nil, errors.New(
			"attempt executor requires a provider queue registry",
		)
	}

	// 如果存在需要 API Key 鉴权的 Provider，
	// 那么 Provider Key 注册表就不能为空。
	//
	// KeyedProviderIDs 返回所有需要 API Key 的 Provider ID。
	if keys == nil && len(adapters.KeyedProviderIDs()) > 0 {
		return nil, errors.New(
			"attempt executor requires a provider key registry for API-key adapters",
		)
	}

	// 重试策略不能为空，
	// 否则无法决定失败后是否继续尝试。
	if retries == nil {
		return nil, errors.New(
			"attempt executor requires a retry policy",
		)
	}

	// 所有依赖检查通过后，创建执行器。
	return &AttemptExecutor{
		adapters: adapters,
		breakers: breakers,
		queues:   queues,
		keys:     keys,
		retries:  retries,
	}, nil
}

// Execute 在当前 Candidate 对应的 Provider 内执行请求。
//
// 它可能调用 executeOnce 多次，
// 直到成功、不可重试、达到重试上限或上下文被取消。
func (executor *AttemptExecutor) Execute(
	ctx context.Context,
	input AttemptInput,
) (AttemptResult, *Failure) {
	// 先创建结果对象。
	//
	// 即使后续请求失败，
	// 也会返回当前 Candidate 的 Provider 和模型信息。
	result := AttemptResult{
		ProviderID:    input.Candidate.ProviderID,
		UpstreamModel: input.Candidate.UpstreamModel,
	}

	// 根据当前 Provider ID 获取对应的重试策略。
	//
	// 不同 Provider 可以拥有不同的：
	// 最大尝试次数；
	// 单次请求超时；
	// 退避时间。
	retry := executor.retries.ForProvider(
		input.Candidate.ProviderID,
	)

	// 当前 Provider 没有配置重试策略，
	// 这是网关本地配置错误，不能继续执行。
	if retry == nil {
		return result, &Failure{
			Category: CategoryLocalValidation,

			ProviderID: input.Candidate.ProviderID,

			// Candidate 记录当前 Provider 在路由计划中的优先级。
			Candidate: input.Candidate.Priority,

			Cause: errors.New(
				"provider retry policy is not configured",
			),
		}
	}

	// preferredKeyID 是下一次重试时优先继续使用的 Key ID。
	//
	// 初始为空，表示第一次调用由 Key 选择器正常选择。
	preferredKeyID := ""

	// excludedKeyIDs 保存当前重试过程中禁止再次选择的 Key。
	//
	// 例如某个 Key 返回 401 或 403，
	// 后续重试会把这个 Key 排除。
	//
	// map 的 key 是 Key ID；
	// struct{} 不保存业务数据，只表示“这个 Key 存在于排除集合中”。
	excludedKeyIDs := make(map[string]struct{})

	// 不断执行单次尝试，
	// 直到成功或重试策略决定停止。
	for {
		// 计算准备执行的是第几次尝试。
		//
		// result.Attempts 只记录已经实际调用过上游的次数，
		// 因此这里先加一作为本轮的尝试编号。
		attempt := result.Attempts + 1

		// 执行一次完整的 Provider 调用流程。
		outcome := executor.executeOnce(
			ctx,
			input,
			attempt,
			preferredKeyID,
			excludedKeyIDs,
			retry,
		)

		// 如果本轮选择过 Key，
		// 就把它保存为结果中的最后使用 Key。
		if outcome.KeyID != "" {
			result.KeyID = outcome.KeyID
		}

		// Attempted 表示这次是否真正执行了 Adapter.Complete。
		//
		// 只有真正调用了上游，才增加 Attempts 并写入 Trail。
		if outcome.Attempted {
			// 实际上游调用次数加一。
			result.Attempts++

			// 保存本次真实调用记录。
			result.Trail = append(
				result.Trail,
				AttemptRecord{
					ProviderID: input.Candidate.ProviderID,

					UpstreamModel: input.Candidate.UpstreamModel,

					KeyID: outcome.KeyID,

					Candidate: input.Candidate.Priority,

					// 这里使用递增后的 Attempts，
					// 表示这是第几次真实上游调用。
					Attempt: result.Attempts,

					Duration: outcome.Duration,

					// 成功时返回空 Category；
					// 失败时返回具体错误分类。
					Category: failureCategory(
						outcome.Failure,
					),

					// 没有状态码时返回 0。
					StatusCode: failureStatus(
						outcome.Failure,
					),
				},
			)
		}

		// 取得本次执行结果中的错误。
		failure := outcome.Failure

		// failure 为空表示请求成功。
		if failure == nil {
			// 保存成功响应体。
			result.Body = outcome.Body

			// 返回最终结果，不再重试。
			return result, nil
		}

		// 如果本轮没有真正调用上游，
		// failure.Attempt 使用当前已经真实执行过的次数。
		//
		// 例如熔断器直接拒绝时，
		// result.Attempts 可能仍然是 0。
		if !outcome.Attempted {
			failure.Attempt = result.Attempts
		}

		// 如果 Failure 内部还没有 Key ID，
		// 就补充最后一次选中的 Key ID。
		if failure.KeyID == "" {
			failure.KeyID = result.KeyID
		}

		// 记录当前 Provider 内已经真实调用过上游的总次数。
		failure.TotalAttempts = result.Attempts

		// 根据失败类型和当前尝试次数，
		// 判断是否还应该继续重试。
		if !retry.ShouldRetry(failure, result.Attempts) {
			// 不可重试或已经达到上限，
			// 返回当前失败。
			return result, failure
		}

		// 根据失败分类产生的控制信号，
		// 判断下次重试是否需要更换 API Key。
		if SignalsFor(failure).SwitchKey {
			// 如果当前 Key 返回 401 或 403，
			// 说明该 Key 本身无效或权限不足。
			//
			// 将它加入排除集合，
			// 避免当前 Execute 后续又选到同一个 Key。
			if outcome.KeyID != "" &&
				(failure.Category == CategoryKeyUnauthorized ||
					failure.Category == CategoryKeyForbidden) {
				excludedKeyIDs[outcome.KeyID] = struct{}{}
			}

			// 清空优先 Key，
			// 下一次重试重新选择其他 Key。
			preferredKeyID = ""
		} else if outcome.KeyID != "" {
			// 如果这次失败不要求切换 Key，
			// 下次重试优先继续使用当前 Key。
			//
			// 例如普通网络超时不一定代表 Key 有问题。
			preferredKeyID = outcome.KeyID
		}

		// 根据失败类型和当前尝试次数计算退避时间，
		// 然后等待下一次重试。
		//
		// Wait 返回 false 表示等待被 ctx 取消或截止时间结束。
		backoff := retry.Backoff(failure, result.Attempts)
		addRetryEvent(
			ctx,
			input.Candidate.ProviderID,
			input.Candidate.Priority,
			result.Attempts,
			failure,
			backoff,
		)
		if !retry.Wait(ctx, backoff) {
			// 如果 ctx 确实存在取消或超时错误，
			// 将它转换成统一 Failure。
			if err := ctx.Err(); err != nil {
				failure = FromProvider(
					ctx,
					input.Candidate.ProviderID,
					input.Candidate.Priority,
					result.Attempts,
					err,
				)
			}

			// 补充最终总尝试次数。
			failure.TotalAttempts = result.Attempts

			// 返回取消、超时或原来的失败。
			return result, failure
		}
	}
}

// attemptOutcome 是 executeOnce 的内部返回结果。
//
// 它只在本文件内部使用，
// 用来告诉 Execute 本轮发生了什么。
type attemptOutcome struct {
	// Body 是 Adapter 返回的响应体。
	Body []byte

	// Failure 是统一分类后的失败。
	//
	// nil 表示成功。
	Failure *Failure

	// KeyID 是本轮实际选择的 Provider Key ID。
	KeyID string

	// Attempted 表示是否真正调用了 Adapter.Complete。
	//
	// 熔断拒绝、排队失败、没有可用 Key 等情况为 false。
	Attempted bool

	// Duration 是 Adapter.Complete 的执行耗时。
	Duration time.Duration
}

// executeOnce 执行当前 Provider 的一次完整尝试。
//
// 一次执行顺序是：
//
// 查找 Adapter
//
//	↓
//
// 检查熔断器
//
//	↓
//
// 获取 Provider 并发队列位置
//
//	↓
//
// 选择 API Key
//
//	↓
//
// 创建单次尝试超时 Context
//
//	↓
//
// 调用 Adapter.Complete
//
//	↓
//
// 分类错误
//
//	↓
//
// 反馈 Key 状态和熔断器状态
func (executor *AttemptExecutor) executeOnce(
	// ctx 是整个请求的总上下文。
	ctx context.Context,

	// input 包含当前请求和候选 Provider 信息。
	input AttemptInput,

	// attempt 是本轮尝试编号。
	attempt int,

	// preferredKeyID 是本轮优先选择的 Key。
	preferredKeyID string,

	// excludedKeyIDs 是本轮禁止选择的 Key 集合。
	excludedKeyIDs map[string]struct{},

	// retry 是当前 Provider 的重试策略。
	retry *RetryPolicy,
) (outcome attemptOutcome) {
	// 取出当前路由候选信息，
	// 避免后面反复写 input.Candidate。
	candidate := input.Candidate
	traceContext, attemptSpan := startAttemptSpan(ctx, input, attempt, false)
	defer func() {
		finishAttemptSpan(attemptSpan, outcome.Failure, outcome.Attempted)
	}()

	// 根据 Provider ID 查找对应 Adapter。
	adapter, ok := executor.adapters.Adapter(
		candidate.ProviderID,
	)

	// 找不到 Adapter 说明本地注册配置不完整。
	if !ok {
		return attemptOutcome{
			Failure: &Failure{
				Category: CategoryLocalValidation,

				ProviderID: candidate.ProviderID,

				Candidate: candidate.Priority,

				Attempt: attempt,

				Cause: provider.ErrUnknownProvider,
			},
		}
	}

	// 请求熔断器判断当前 Provider 是否允许调用。
	//
	// 允许时返回 Permit；
	// 熔断器处于 open 状态时返回 Failure。
	permit, failure := executor.breakers.Allow(
		candidate.ProviderID,
	)

	// 熔断器拒绝请求时，
	// 不再进入排队和上游调用。
	if failure != nil {
		// 补充当前候选优先级。
		failure.Candidate = candidate.Priority

		// 补充本轮尝试编号。
		failure.Attempt = attempt

		return attemptOutcome{
			Failure: failure,
		}
	}

	// 如果函数中途提前返回且还没有 Complete，
	// 自动放弃这个 Permit。
	//
	// 如果后面已经调用 permit.Complete，
	// completed 原子标记已经变成 true，
	// 此处 Abandon 会返回 false，不会重复修改熔断器。
	defer permit.Abandon()

	// 从当前 Provider 的队列中获取一个并发执行位置。
	//
	// 如果并发已满，可能等待；
	// 等待队列也满或等待超时时，会返回 Failure。
	lease, failure := traceQueueAcquire(
		traceContext,
		executor.queues,
		candidate.ProviderID,
	)

	// 获取并发位置失败时，
	// 本轮不会调用真实上游。
	if failure != nil {
		failure.Candidate = candidate.Priority
		failure.Attempt = attempt

		// 将排队失败反馈给熔断器。
		//
		// 是否计入熔断由 SignalsFor(failure).CountBreaker 决定。
		permit.Complete(failure)

		return attemptOutcome{
			Failure: failure,
		}
	}

	// 函数结束时释放当前 Provider 的并发位置，
	// 让队列中的其他请求继续执行。
	defer lease.Release()

	// selectedKey 保存当前选中的 Provider API Key。
	//
	// 不需要 API Key 的 Provider 会一直保持 nil。
	var selectedKey *ProviderKeySelection

	// 只有同时满足以下条件才选择 Key：
	//
	// 1. 系统存在 ProviderKeyRegistry；
	// 2. 当前 Adapter 使用 API Key 鉴权。
	if executor.keys != nil &&
		adapter.Authentication() ==
			provider.AuthenticationAPIKey {
		// 从当前 Provider 的 Key 池中选择一个 Key。
		//
		// preferredKeyID：优先继续使用哪个 Key；
		// excludedKeyIDs：哪些 Key 当前不能再选。
		selectedKey, failure = executor.keys.selectKey(
			candidate.ProviderID,
			preferredKeyID,
			excludedKeyIDs,
		)

		// 没有可用 Key 或 Key 选择失败时，
		// 不调用真实上游。
		if failure != nil {
			failure.Candidate = candidate.Priority
			failure.Attempt = attempt

			// 将 Key 选择失败反馈给熔断器。
			permit.Complete(failure)

			return attemptOutcome{
				Failure: failure,
			}
		}
	}

	// 基于总请求 ctx 创建本次尝试专用 Context。
	//
	// 它通常会叠加 AttemptTimeout，
	// 防止单次上游调用占满整个总请求时间。
	attemptContext, cancelAttempt :=
		retry.AttemptContext(traceContext)

	// 函数结束时释放本次尝试 Context 的资源。
	defer cancelAttempt()

	// modelOverride 表示是否需要覆盖客户端原始模型名。
	//
	// 默认空字符串表示不覆盖。
	modelOverride := ""

	// 如果路由选出的真实上游模型与客户端请求模型不同，
	// 就把 Candidate.UpstreamModel 作为覆盖模型。
	//
	// 例如：
	// RequestedModel = "company-default"
	// UpstreamModel  = "gpt-5"
	if candidate.UpstreamModel != input.RequestedModel {
		modelOverride = candidate.UpstreamModel
	}

	// apiKey 是最终传给 Adapter 的真实 Key Secret。
	//
	// 不需要 API Key 的 Provider 保持空字符串。
	apiKey := ""

	// 如果选择到了 Key，
	// 读取它的 Secret。
	if selectedKey != nil {
		apiKey = selectedKey.Secret()
		setAttemptKeyID(attemptSpan, selectedKey.KeyID())
	}

	// 记录真正调用上游前的开始时间。
	startedAt := time.Now()

	// 调用当前 Provider Adapter。
	//
	// Adapter 根据 Provider 协议：
	// 组装 URL；
	// 组装请求头；
	// 转换请求格式；
	// 发出 HTTP 请求；
	// 返回响应体或错误。
	responseBody, err := adapter.Complete(
		attemptContext,
		provider.ChatInput{
			// 继续传递网关请求 ID。
			RequestID: input.RequestID,

			// 传入统一聊天请求。
			Request: input.Request,

			// 必要时覆盖为真实上游模型。
			ModelOverride: modelOverride,
		},
		// 传入当前选中的 API Key Secret。
		apiKey,
	)

	// 计算本次 Adapter 调用的总耗时。
	duration := time.Since(startedAt)

	// 将 Adapter 返回的 err 转换为网关统一 Failure。
	//
	// err 为 nil 时，failure 也为 nil。
	failure = FromProvider(
		ctx,
		candidate.ProviderID,
		candidate.Priority,
		attempt,
		err,
	)

	// selectedKeyID 保存本轮使用的 Key ID。
	//
	// 它只用于日志和状态反馈，不包含 Secret。
	selectedKeyID := ""

	// 如果本轮使用了 Provider Key，
	// 就将执行结果反馈给 Key 选择器。
	if selectedKey != nil {
		// 取得 Key 的公开标识。
		selectedKeyID = selectedKey.KeyID()

		// 请求失败时，在 Failure 中记录是哪一个 Key。
		if failure != nil {
			failure.KeyID = selectedKeyID
		}

		// 通知 Key 选择器本次调用结果。
		//
		// 例如：
		// 429 可能让 Key 进入冷却；
		// 401/403 可能标记 Key 鉴权失败；
		// 成功则记录正常完成。
		selectedKey.Complete(failure)
	}

	// 将最终成功或失败反馈给熔断器。
	//
	// failure 为 nil 表示成功；
	// 非 nil 时是否计入熔断由 Failure 信号决定。
	permit.Complete(failure)

	// 判断本轮是否应计作一次真实尝试。
	//
	// Adapter.Complete 虽然已经被调用，
	// 但以下错误表示请求在本地准备阶段就失败了：
	//
	// ErrInvalidRequest：
	// 请求无法在本地构造；
	//
	// ErrUnsupportedCapability：
	// 当前 Provider 不支持请求所需能力。
	//
	// 这两种情况没有真正发送有效上游请求，
	// 因此不计入 Attempts。
	attempted :=
		!errors.Is(err, provider.ErrInvalidRequest) &&
			!errors.Is(
				err,
				provider.ErrUnsupportedCapability,
			)

	// 返回本轮执行结果，
	// 由外层 Execute 决定成功返回还是继续重试。
	return attemptOutcome{
		Body: responseBody,

		Failure: failure,

		KeyID: selectedKeyID,

		Attempted: attempted,

		Duration: duration,
	}
}

// failureCategory 从 Failure 中安全取得错误分类。
//
// 主要用于生成 AttemptRecord。
func failureCategory(failure *Failure) Category {
	// 成功时 failure 为 nil，
	// 返回空分类。
	if failure == nil {
		return ""
	}

	// 返回统一错误分类。
	return failure.Category
}

// failureStatus 从 Failure 中安全取得 HTTP 状态码。
//
// 主要用于生成 AttemptRecord。
func failureStatus(failure *Failure) int {
	// 成功或者没有 Failure 时返回 0。
	if failure == nil {
		return 0
	}

	// 返回上游 HTTP 状态码。
	return failure.StatusCode
}
