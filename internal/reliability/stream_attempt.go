package reliability // 可靠性控制包。

import (
	"context"     // 控制流式请求取消和超时。
	"errors"      // 判断和创建错误。
	"fmt"         // 包装流格式错误。
	"io"          // 使用 EOF 判断流结束。
	"sync/atomic" // 保证流只被结束一次。
	"time"        // 记录耗时并控制首事件超时。

	"go.opentelemetry.io/otel/trace"

	"model-velo/internal/provider" // Provider 流接口和事件类型。
)

// PreparedStream 在首个内容事件通过校验后接管上游流及可靠性资源。
// Next 和 Finish 由同一调用方协调；Finish 本身允许被重复调用。
type PreparedStream struct { // 已成功建立并读取到首事件的流。
	FirstEvent      provider.ChatStreamEvent // 已提前读取并验证的首个事件。
	ProviderID      string                   // 当前成功建流的 Provider ID。
	UpstreamModel   string                   // 实际调用的上游模型。
	KeyID           string                   // 本次使用的 Provider Key ID。
	Candidate       int                      // 当前候选 Provider 的优先级。
	Attempts        int                      // 建流阶段累计尝试次数。
	Fallbacks       int                      // 建流阶段切换 Provider 的次数。
	CandidatesTried int                      // 已尝试的候选 Provider 数量。
	Trail           []AttemptRecord          // 建流阶段的尝试记录。

	stream   *provider.ChatEventStream // 后续继续读取事件的上游流。
	permit   *Permit                   // 需要在结束时反馈结果的熔断许可。
	lease    *QueueLease               // 当前占用的 Provider 队列槽位。
	key      *ProviderKeySelection     // 当前选中的 Provider Key。
	cancel   context.CancelFunc        // 用于取消上游流 Context。
	idleTime time.Duration             // 后续两个有效事件之间允许的最长静默时间。
	started  time.Time                 // 最终成功 Attempt 实际调用上游的开始时间。
	span     trace.Span                // 覆盖首事件到流终态的 Provider Attempt Span。
	finished atomic.Bool               // 标记流是否已经结束并释放资源。
	done     bool                      // 标记是否已经收到 [DONE]。
	terminal atomic.Pointer[streamTerminal]
}

type streamTerminal struct {
	failure  *Failure
	duration time.Duration
}

func (stream *PreparedStream) Next() (provider.ChatStreamEvent, error) { // 读取首事件之后的下一个上游事件。
	if stream == nil || stream.stream == nil || stream.finished.Load() { // 流无效或已经结束。
		return provider.ChatStreamEvent{}, io.EOF // 按已读完处理。
	}
	if stream.idleTime <= 0 || stream.cancel == nil {
		return stream.next()
	}

	result := make(chan streamEventResult, 1)
	go func() {
		event, err := stream.next()
		result <- streamEventResult{event: event, err: err}
	}()

	timer := time.NewTimer(stream.idleTime)
	defer stopRetryTimer(timer)
	for {
		select {
		case outcome := <-result:
			return outcome.event, outcome.err
		case <-stream.stream.Activity():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(stream.idleTime)
		case <-timer.C:
			stream.cancel()
			<-result
			return provider.ChatStreamEvent{}, fmt.Errorf("stream idle timeout: %w", context.DeadlineExceeded)
		}
	}
}

type streamEventResult struct {
	event provider.ChatStreamEvent
	err   error
}

func (stream *PreparedStream) next() (provider.ChatStreamEvent, error) {
	event, err := stream.stream.Next()          // 从上次位置继续读取下一个 SSE 事件。
	if errors.Is(err, io.EOF) && !stream.done { // 未收到 DONE 就直接结束。
		return provider.ChatStreamEvent{}, fmt.Errorf("%w: stream ended before done", provider.ErrInvalidStream) // 归类为非法流。
	}
	if err == nil && event.Done { // 成功读取到结束事件。
		stream.done = true // 记录已经收到 DONE。
	}
	return event, err // 返回当前事件或读取错误。
}

// Finish 关闭上游响应，将最终结果回写 Key 和 Breaker，再释放 Queue 槽位。
func (stream *PreparedStream) Finish(failure *Failure) bool { // 结束流并统一释放可靠性资源。
	if stream == nil || !stream.finished.CompareAndSwap(false, true) { // 流为空或已经结束过。
		return false // 拒绝重复结束。
	}
	stream.describeFailure(failure) // 给错误补充 Provider、Key 和尝试信息。
	if stream.cancel != nil {       // 存在上游取消函数。
		stream.cancel() // 取消上游流 Context。
	}
	if stream.stream != nil { // 上游流已经建立。
		_ = stream.stream.Close() // 关闭响应体，忽略重复关闭错误。
	}
	stream.key.Complete(failure)    // 将最终结果反馈给 Provider Key。
	stream.permit.Complete(failure) // 将最终结果反馈给熔断器。
	stream.lease.Release()          // 释放 Provider 并发槽位。
	finishAttemptSpan(stream.span, failure, true)
	stream.terminal.Store(&streamTerminal{
		failure:  failure,
		duration: time.Since(stream.started),
	})
	return true // 表示本次成功结束流。
}

// FinishError 将提交后的上游读取错误归入既有可靠性分类并结束当前流。
func (stream *PreparedStream) FinishError(ctx context.Context, err error) *Failure { // 转换流读取错误并释放资源。
	if stream == nil || err == nil { // 没有流或没有错误。
		return nil // 无需处理。
	}
	failure := FromProvider( // 将底层错误转换为统一 Failure。
		ctx,                     // 当前客户端请求 Context。
		stream.ProviderID,       // 发生错误的 Provider。
		stream.Candidate,        // 当前候选优先级。
		stream.currentAttempt(), // 当前尝试编号。
		err,                     // 上游流读取错误。
	)
	stream.Finish(failure) // 反馈错误并关闭流。
	return failure         // 返回统一错误。
}

// Abort 表示客户端传输或网关主动终止，不把结果归因于 Provider。
func (stream *PreparedStream) Abort(cause error) bool { // 主动取消流。
	if cause == nil { // 调用方没有提供原因。
		cause = context.Canceled // 默认按请求取消处理。
	}
	return stream.Finish(&Failure{Category: CategoryCanceled, Cause: cause}) // 以取消分类结束流。
}

func (stream *PreparedStream) describeFailure(failure *Failure) { // 给 Failure 补充完整执行信息。
	if stream == nil || failure == nil { // 流或错误为空。
		return // 无需补充。
	}
	failure.ProviderID = stream.ProviderID                        // 记录当前 Provider。
	failure.KeyID = stream.KeyID                                  // 记录当前 Key。
	failure.Candidate = stream.Candidate                          // 记录候选优先级。
	failure.Attempt = stream.currentAttempt()                     // 记录最后一次尝试编号。
	failure.TotalAttempts = stream.Attempts                       // 记录累计尝试次数。
	failure.Fallbacks = stream.Fallbacks                          // 记录累计回退次数。
	failure.Trail = append([]AttemptRecord(nil), stream.Trail...) // 复制完整尝试记录。
}

func (stream *PreparedStream) currentAttempt() int { // 获取当前流对应的最后尝试编号。
	if stream == nil { // 流不存在。
		return 0 // 返回零值。
	}
	if len(stream.Trail) > 0 { // 存在尝试记录。
		return stream.Trail[len(stream.Trail)-1].Attempt // 返回最后一条记录的尝试编号。
	}
	return stream.Attempts // 没有 Trail 时使用累计尝试数。
}

// FinalAttempt returns the successful preparation attempt with its real stream
// terminal result. It is available only after Finish or Abort.
func (stream *PreparedStream) FinalAttempt() (AttemptRecord, bool) {
	if stream == nil || len(stream.Trail) == 0 {
		return AttemptRecord{}, false
	}
	terminal := stream.terminal.Load()
	if terminal == nil {
		return AttemptRecord{}, false
	}
	record := stream.Trail[len(stream.Trail)-1]
	record.Category = failureCategory(terminal.failure)
	record.StatusCode = failureStatus(terminal.failure)
	if !stream.started.IsZero() {
		record.Duration = terminal.duration
	}
	return record, true
}

// PrepareStream 在单个候选内执行有限 Retry，并在不提交客户端响应的前提下
// 缓冲首个有效内容事件。跨候选 Fallback 仍由 Orchestrator 决定。
func (executor *AttemptExecutor) PrepareStream( // 对单个候选 Provider 建立流。
	ctx context.Context, // 同时作为建流预算和后续流的父 Context。
	input AttemptInput, // 当前请求及候选 Provider。
) (*PreparedStream, *Failure) {
	result, failure := executor.prepareStream(ctx, ctx, input) // 在当前候选内执行重试建流。
	if failure != nil {                                        // 最终建流失败。
		failure.TotalAttempts = result.attempts                       // 补充累计尝试次数。
		failure.Trail = append([]AttemptRecord(nil), result.trail...) // 补充尝试记录副本。
	} else { // 当前候选成功建流。
		result.stream.CandidatesTried = 1 // 单候选接口只尝试了一个 Candidate。
	}
	return result.stream, failure // 返回准备好的流或最终错误。
}

type streamAttemptResult struct { // 单候选内多次建流尝试的汇总结果。
	stream   *PreparedStream // 成功时得到的流。
	keyID    string          // 最后选择的 Key ID。
	attempts int             // 实际建流尝试次数。
	trail    []AttemptRecord // 每次建流尝试记录。
}

func (executor *AttemptExecutor) prepareStream( // 在单个候选 Provider 内执行流式重试。
	ctx context.Context, // 控制建流和首事件总预算。
	streamParent context.Context, // 成功后上游流继续使用的父 Context。
	input AttemptInput, // 当前请求和候选 Provider。
) (streamAttemptResult, *Failure) {
	candidate := input.Candidate           // 取出当前路由候选。
	result := streamAttemptResult{}        // 初始化本候选执行结果。
	if ctx == nil || streamParent == nil { // 任一 Context 不存在。
		return result, &Failure{
			Category:   CategoryCanceled,                            // 按取消错误处理。
			ProviderID: candidate.ProviderID,                        // 记录当前 Provider。
			Candidate:  candidate.Priority,                          // 记录候选优先级。
			Cause:      errors.New("stream attempt context is nil"), // 记录具体原因。
		}
	}
	retry := executor.retries.ForProvider(candidate.ProviderID) // 获取当前 Provider 的重试策略。
	if retry == nil {                                           // 没有配置重试策略。
		return result, &Failure{
			Category:   CategoryLocalValidation,                               // 本地配置错误。
			ProviderID: candidate.ProviderID,                                  // 当前 Provider。
			Candidate:  candidate.Priority,                                    // 当前候选优先级。
			Cause:      errors.New("provider retry policy is not configured"), // 具体原因。
		}
	}

	preferredKeyID := ""                        // 下一次重试优先继续使用的 Key。
	excludedKeyIDs := make(map[string]struct{}) // 当前重试过程禁止再次选择的 Key。
	for {                                       // 持续尝试直到成功或不能重试。
		attempt := result.attempts + 1         // 计算本轮尝试编号。
		outcome := executor.prepareStreamOnce( // 执行一次建流和首事件读取。
			ctx,            // 建流总预算 Context。
			streamParent,   // 成功流的父 Context。
			input,          // 请求和候选 Provider。
			attempt,        // 本轮尝试编号。
			preferredKeyID, // 优先 Key。
			excludedKeyIDs, // 排除 Key 集合。
			retry,          // 当前 Provider 重试策略。
		)
		if outcome.KeyID != "" { // 本轮选择到了 Key。
			result.keyID = outcome.KeyID // 保存最后使用的 Key ID。
		}
		if outcome.Attempted { // 本轮真正尝试了上游建流。
			result.attempts++                                  // 累计尝试次数加一。
			result.trail = append(result.trail, AttemptRecord{ // 记录本次建流尝试。
				ProviderID:    candidate.ProviderID,             // 当前 Provider。
				UpstreamModel: candidate.UpstreamModel,          // 当前上游模型。
				KeyID:         outcome.KeyID,                    // 本轮 Key ID。
				Candidate:     candidate.Priority,               // 候选优先级。
				Attempt:       result.attempts,                  // 实际尝试编号。
				Duration:      outcome.Duration,                 // 建流和首事件耗时。
				Category:      failureCategory(outcome.Failure), // 失败分类，成功时为空。
				StatusCode:    failureStatus(outcome.Failure),   // 上游状态码。
			})
		}

		failure := outcome.Failure // 取得本轮错误。
		if failure == nil {        // 建流并取得首事件成功。
			result.stream = outcome.Stream                                      // 保存已经准备好的流。
			result.stream.Attempts = result.attempts                            // 写入本候选累计尝试次数。
			result.stream.Trail = append([]AttemptRecord(nil), result.trail...) // 写入尝试记录副本。
			return result, nil                                                  // 返回成功结果。
		}
		if !outcome.Attempted { // 本轮没有真正调用上游。
			failure.Attempt = result.attempts // 使用已完成的实际尝试次数。
		}
		if failure.KeyID == "" { // Failure 尚未记录 Key。
			failure.KeyID = result.keyID // 补充最后使用的 Key。
		}
		failure.TotalAttempts = result.attempts           // 补充累计尝试次数。
		if !retry.ShouldRetry(failure, result.attempts) { // 错误不可重试或达到上限。
			return result, failure // 返回最终失败。
		}

		if SignalsFor(failure).SwitchKey { // 当前错误要求切换 Key。
			if outcome.KeyID != "" && (failure.Category == CategoryKeyUnauthorized || failure.Category == CategoryKeyForbidden) { // Key 返回 401 或 403。
				excludedKeyIDs[outcome.KeyID] = struct{}{} // 当前 Execute 内不再选择该 Key。
			}
			preferredKeyID = "" // 清除优先 Key，让选择器换 Key。
		} else if outcome.KeyID != "" { // 当前错误不要求换 Key。
			preferredKeyID = outcome.KeyID // 下次优先继续使用当前 Key。
		}
		backoff := retry.Backoff(failure, result.attempts) // 计算本轮退避时间。
		addRetryEvent(                                     // 在当前请求 Span 上记录流式重试。
			ctx,
			candidate.ProviderID,
			candidate.Priority,
			result.attempts,
			failure,
			backoff,
		)
		if !retry.Wait(ctx, backoff) { // 按退避时间等待下一次尝试。
			if err := ctx.Err(); err != nil { // Context 已取消或超时。
				failure = FromProvider(ctx, candidate.ProviderID, candidate.Priority, result.attempts, err) // 转换为统一 Failure。
			}
			failure.TotalAttempts = result.attempts // 补充最终尝试次数。
			return result, failure                  // 停止重试。
		}
	}
}

type streamAttemptOutcome struct { // 单次建流尝试的返回结果。
	Stream    *PreparedStream // 成功时准备好的流。
	Failure   *Failure        // 失败时的统一错误。
	KeyID     string          // 本轮使用的 Key ID。
	Attempted bool            // 是否真正尝试调用上游。
	Duration  time.Duration   // 建流并读取首事件的耗时。
}

func (executor *AttemptExecutor) prepareStreamOnce( // 执行一次完整的建流尝试。
	ctx context.Context, // 建流阶段 Context。
	streamParent context.Context, // 成功流后续运行的父 Context。
	input AttemptInput, // 当前请求和候选 Provider。
	attempt int, // 本轮尝试编号。
	preferredKeyID string, // 优先使用的 Key ID。
	excludedKeyIDs map[string]struct{}, // 禁止选择的 Key 集合。
	retry *RetryPolicy, // 当前 Provider 重试策略。
) (outcome streamAttemptOutcome) {
	candidate := input.Candidate // 取出当前候选 Provider。
	traceContext, attemptSpan := startAttemptSpan(ctx, input, attempt, true)
	spanHandedOff := false
	defer func() {
		if !spanHandedOff {
			finishAttemptSpan(attemptSpan, outcome.Failure, outcome.Attempted)
		}
	}()
	adapter, ok := executor.adapters.Adapter(candidate.ProviderID) // 查找当前 Provider Adapter。
	if !ok {                                                       // Adapter 未注册。
		return streamAttemptOutcome{Failure: &Failure{
			Category:   CategoryLocalValidation,     // 本地配置错误。
			ProviderID: candidate.ProviderID,        // 当前 Provider。
			Candidate:  candidate.Priority,          // 当前候选优先级。
			Attempt:    attempt,                     // 当前尝试编号。
			Cause:      provider.ErrUnknownProvider, // Provider 未知。
		}}
	}
	streamingAdapter, ok := adapter.(provider.StreamingAdapter) // 判断 Adapter 是否实现流式接口。
	if !ok {                                                    // 当前 Provider Adapter 不支持流式调用。
		return streamAttemptOutcome{Failure: FromProvider(
			ctx,                               // 当前 Context。
			candidate.ProviderID,              // 当前 Provider。
			candidate.Priority,                // 候选优先级。
			attempt,                           // 尝试编号。
			provider.ErrUnsupportedCapability, // 不支持流式能力。
		)}
	}

	permit, failure := executor.breakers.Allow(candidate.ProviderID) // 请求熔断器许可。
	if failure != nil {                                              // Provider 当前被熔断。
		failure.Candidate = candidate.Priority        // 补充候选优先级。
		failure.Attempt = attempt                     // 补充尝试编号。
		return streamAttemptOutcome{Failure: failure} // 返回熔断错误。
	}
	handedOff := false // 标记资源是否已经交给 PreparedStream 管理。
	defer func() {     // 函数结束时处理未移交的熔断许可。
		if !handedOff { // 建流失败，资源未交给 PreparedStream。
			permit.Abandon() // 放弃熔断许可。
		}
	}()

	lease, failure := traceQueueAcquire( // 获取当前 Provider 的并发槽位并记录等待 Span。
		traceContext,
		executor.queues,
		candidate.ProviderID,
	)
	if failure != nil { // 排队或获取槽位失败。
		failure.Candidate = candidate.Priority        // 补充候选优先级。
		failure.Attempt = attempt                     // 补充尝试编号。
		permit.Complete(failure)                      // 将失败反馈给熔断器。
		return streamAttemptOutcome{Failure: failure} // 返回队列错误。
	}
	defer func() { // 函数结束时处理未移交的队列槽位。
		if !handedOff { // 建流失败，槽位未交给 PreparedStream。
			lease.Release() // 立即释放并发槽位。
		}
	}()

	var selectedKey *ProviderKeySelection                                                  // 保存当前选中的 Provider Key。
	if executor.keys != nil && adapter.Authentication() == provider.AuthenticationAPIKey { // 当前 Provider 使用 API Key 鉴权。
		selectedKey, failure = executor.keys.selectKey(candidate.ProviderID, preferredKeyID, excludedKeyIDs) // 选择可用 Key。
		if failure != nil {                                                                                  // 没有可用 Key 或选择失败。
			failure.Candidate = candidate.Priority        // 补充候选优先级。
			failure.Attempt = attempt                     // 补充尝试编号。
			permit.Complete(failure)                      // 将失败反馈给熔断器。
			return streamAttemptOutcome{Failure: failure} // 返回 Key 选择错误。
		}
	}

	modelOverride := ""                                  // 默认不覆盖请求模型。
	if candidate.UpstreamModel != input.RequestedModel { // 上游模型与客户端模型不同。
		modelOverride = candidate.UpstreamModel // 使用路由指定的真实上游模型。
	}
	apiKey := ""            // 默认不传 API Key。
	if selectedKey != nil { // 已选择 Provider Key。
		apiKey = selectedKey.Secret() // 取得真正的 Key Secret。
		setAttemptKeyID(attemptSpan, selectedKey.KeyID())
	}

	startedAt := time.Now() // 记录建流开始时间。
	streamContext := trace.ContextWithSpan(streamParent, attemptSpan)
	upstream, firstEvent, cancel, err := openFirstStreamEvent( // 打开上游流并读取首个有效事件。
		traceContext,     // 控制等待和总预算并传播 Attempt Span。
		streamContext,    // 成功后沿用客户端生命周期和 Attempt Span。
		streamingAdapter, // 当前 Provider 的流式 Adapter。
		provider.ChatInput{
			RequestID:     input.RequestID, // 网关请求 ID。
			Request:       input.Request,   // 客户端聊天请求。
			ModelOverride: modelOverride,   // 必要时覆盖模型名。
		},
		apiKey,                 // 当前 Provider API Key。
		retry.AttemptTimeout(), // 单次建流和首事件超时。
	)
	duration := time.Since(startedAt)                                                   // 计算建流和首事件耗时。
	failure = FromProvider(ctx, candidate.ProviderID, candidate.Priority, attempt, err) // 将底层错误转换为统一 Failure。
	keyID := ""                                                                         // 默认没有使用 Key。
	if selectedKey != nil {                                                             // 本轮选择过 Key。
		keyID = selectedKey.KeyID() // 取得 Key 的公开 ID。
		if failure != nil {         // 本轮建流失败。
			failure.KeyID = keyID // 在 Failure 中记录 Key ID。
		}
	}
	if failure != nil { // 建流或首事件读取失败。
		selectedKey.Complete(failure)                                                                                  // 将失败反馈给 Provider Key。
		permit.Complete(failure)                                                                                       // 将失败反馈给熔断器。
		attempted := !errors.Is(err, provider.ErrInvalidRequest) && !errors.Is(err, provider.ErrUnsupportedCapability) // 本地准备错误不算真实上游尝试。
		return streamAttemptOutcome{
			Failure:   failure,   // 返回统一错误。
			KeyID:     keyID,     // 返回本轮 Key ID。
			Attempted: attempted, // 标记是否真实尝试上游。
			Duration:  duration,  // 返回本轮耗时。
		}
	}

	handedOff = true // 将流、熔断许可、队列槽位和 Key 交给 PreparedStream。
	spanHandedOff = true
	return streamAttemptOutcome{
		Stream: &PreparedStream{
			FirstEvent:    firstEvent,              // 保存提前读取的首事件。
			ProviderID:    candidate.ProviderID,    // 保存成功 Provider。
			UpstreamModel: candidate.UpstreamModel, // 保存真实上游模型。
			KeyID:         keyID,                   // 保存 Key ID。
			Candidate:     candidate.Priority,      // 保存候选优先级。
			stream:        upstream,                // 保存上游流。
			permit:        permit,                  // 保存熔断许可。
			lease:         lease,                   // 保存队列槽位。
			key:           selectedKey,             // 保存 Provider Key 选择。
			cancel:        cancel,                  // 保存上游流取消函数。
			idleTime:      retry.AttemptTimeout(),  // 复用 Provider 单次超时作为事件静默上限。
			started:       startedAt,               // 保存真实上游 Attempt 开始时间。
			span:          attemptSpan,             // 在流终态结束 Attempt Span。
		},
		KeyID:     keyID,    // 返回本轮 Key ID。
		Attempted: true,     // 已成功调用上游并读取首事件。
		Duration:  duration, // 返回建流耗时。
	}
}

type firstStreamEventResult struct { // 后台建流 goroutine 的返回数据。
	stream      *provider.ChatEventStream // 已打开的上游事件流。
	event       provider.ChatStreamEvent  // 从上游读取的首个事件。
	err         error                     // 建流或首事件读取错误。
	completedAt time.Time                 // 后台任务实际完成时间。
}

func openFirstStreamEvent( // 打开上游流，并在超时内取得首个有效内容事件。
	waitContext context.Context, // 控制调用方等待过程的 Context。
	streamParent context.Context, // 成功后上游流继续使用的父 Context。
	adapter provider.StreamingAdapter, // 当前 Provider 的流式 Adapter。
	input provider.ChatInput, // 已组装的上游聊天输入。
	apiKey string, // 当前 Provider API Key。
	timeout time.Duration, // 建流和首事件最大等待时间。
) (*provider.ChatEventStream, provider.ChatStreamEvent, context.CancelFunc, error) {
	streamContext, cancel := context.WithCancel(streamParent) // 为上游流创建可独立取消的 Context。
	result := make(chan firstStreamEventResult, 1)            // 接收后台建流结果，缓冲防止 goroutine 阻塞。
	deadline := time.Now().Add(timeout)                       // 记录本次尝试的绝对截止时间。
	timer := time.NewTimer(timeout)                           // 创建单次尝试超时计时器。
	defer stopRetryTimer(timer)                               // 函数结束时安全停止并清理计时器。
	go func() {                                               // 后台打开流并阻塞读取首个事件。
		upstream, err := adapter.OpenStream(streamContext, input, apiKey) // 调用 Provider 打开流式响应。
		if err != nil {                                                   // 建流本身失败。
			result <- firstStreamEventResult{err: err, completedAt: time.Now()} // 返回建流错误和完成时间。
			return                                                              // 结束 goroutine。
		}
		firstEvent, err := upstream.Next() // 读取并解析上游首个 SSE 事件。
		result <- firstStreamEventResult{
			stream:      upstream,   // 返回已打开的流。
			event:       firstEvent, // 返回首个事件。
			err:         err,        // 返回首事件读取错误。
			completedAt: time.Now(), // 记录实际完成时间。
		}
	}()

	select { // 等待后台结果、请求取消或尝试超时。
	case outcome := <-result: // 后台建流和首事件读取完成。
		if waitContext.Err() != nil { // 等待期间总 Context 已结束。
			cancel()                                                       // 取消上游流。
			_ = outcome.stream.Close()                                     // 关闭已经打开的响应体。
			return nil, provider.ChatStreamEvent{}, nil, waitContext.Err() // 返回总 Context 错误。
		}
		if !outcome.completedAt.Before(deadline) { // 后台结果实际完成时间已经到达或超过截止时间。
			cancel()                                                              // 取消上游流。
			_ = outcome.stream.Close()                                            // 关闭响应体。
			return nil, provider.ChatStreamEvent{}, nil, context.DeadlineExceeded // 按尝试超时处理。
		}
		if outcome.err == nil && !outcome.event.Done { // 首事件读取成功且不是立即结束事件。
			return outcome.stream, outcome.event, cancel, nil // 返回可继续使用的流和首事件。
		}
		cancel()                                                  // 首事件无效时取消上游流。
		_ = outcome.stream.Close()                                // 关闭上游响应体。
		if outcome.err == nil || errors.Is(outcome.err, io.EOF) { // 上游直接 DONE 或没有任何内容就 EOF。
			outcome.err = fmt.Errorf("%w: stream ended before first content event", provider.ErrInvalidStream) // 转成非法流错误。
		}
		return nil, provider.ChatStreamEvent{}, nil, outcome.err // 返回首事件读取或格式错误。
	case <-waitContext.Done(): // 总请求 Context 先结束。
		cancel()                                                       // 取消后台上游调用。
		outcome := <-result                                            // 等待后台 goroutine 退出并取回资源。
		_ = outcome.stream.Close()                                     // 关闭可能已经建立的流。
		return nil, provider.ChatStreamEvent{}, nil, waitContext.Err() // 返回总请求取消或超时错误。
	case <-timer.C: // 单次建流超时先到达。
		cancel()                                                              // 取消后台上游调用。
		outcome := <-result                                                   // 等待后台 goroutine 结束，避免泄漏。
		_ = outcome.stream.Close()                                            // 关闭可能建立的上游流。
		return nil, provider.ChatStreamEvent{}, nil, context.DeadlineExceeded // 返回单次尝试超时。
	}
}
