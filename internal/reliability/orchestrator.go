package reliability // 可靠性调度包。

import (
	"context" // 传递请求取消和总执行超时。
	"errors"  // 创建启动配置错误。

	"model-velo/internal/provider" // 统一聊天请求结构。
	"model-velo/internal/routing"  // 路由计划和候选 Provider。
)

type ExecutionInput struct { // Orchestrator 执行一次完整请求所需的输入。
	RequestID string               // 当前网关请求 ID。
	Request   provider.ChatRequest // 客户端聊天请求。
	Plan      routing.Plan         // 路由器生成的候选 Provider 计划。
}

type ExecutionResult struct { // 非流式请求最终成功结果。
	Body            []byte          // 最终上游响应体。
	ProviderID      string          // 最终成功的 Provider ID。
	UpstreamModel   string          // 最终调用的真实上游模型。
	KeyID           string          // 最终使用的上游 API Key ID。
	Attempts        int             // 所有候选中实际调用上游的总次数。
	Fallbacks       int             // 切换到下一个 Provider 的次数。
	CandidatesTried int             // 实际尝试过的候选 Provider 数量。
	Trail           []AttemptRecord // 所有上游尝试记录。
}

type Orchestrator struct { // 负责依次执行候选 Provider 并决定是否回退。
	attempts *AttemptExecutor // 负责单个 Provider 内的请求和重试。
	retries  RetryPolicies    // 提供整个请求的总超时策略。
}

func NewOrchestrator(attempts *AttemptExecutor, retries RetryPolicies) (*Orchestrator, error) { // 创建候选 Provider 调度器。
	if attempts == nil { // 单 Provider 执行器不能为空。
		return nil, errors.New("orchestrator requires an attempt executor")
	}
	if retries == nil { // 总请求重试策略不能为空。
		return nil, errors.New("orchestrator requires a retry policy")
	}
	return &Orchestrator{attempts: attempts, retries: retries}, nil // 保存依赖并返回调度器。
}

func (orchestrator *Orchestrator) Execute(ctx context.Context, input ExecutionInput) (ExecutionResult, *Failure) { // 依次尝试候选 Provider，返回第一个成功结果。
	if len(input.Plan.Candidates) == 0 { // 路由计划没有任何候选 Provider。
		return ExecutionResult{}, &Failure{
			Category: CategoryLocalValidation, // 归类为本地路由配置错误。
			Cause:    routing.ErrNoRoute,      // 具体原因是没有可用路由。
		}
	}

	executionContext, cancelExecution := orchestrator.retries.RequestContext(ctx) // 创建受总请求超时约束的 Context。
	defer cancelExecution()                                                       // 函数结束时释放总请求 Context。

	totalAttempts := 0                                    // 累计所有 Provider 的真实上游调用次数。
	fallbacks := 0                                        // 累计切换 Provider 的次数。
	var trail []AttemptRecord                             // 累计所有候选的尝试记录。
	var lastFailure *Failure                              // 保存最后一次失败，供循环结束后返回。
	for index, candidate := range input.Plan.Candidates { // 按路由优先级依次尝试候选 Provider。
		attemptResult, failure := orchestrator.attempts.Execute(executionContext, AttemptInput{ // 在当前 Provider 内执行请求和重试。
			RequestID:      input.RequestID,           // 传递网关请求 ID。
			RequestedModel: input.Plan.RequestedModel, // 传递客户端请求的模型名。
			Request:        input.Request,             // 传递聊天请求。
			Candidate:      candidate,                 // 传递当前候选 Provider。
		})
		totalAttempts += attemptResult.Attempts       // 累加当前 Provider 的真实调用次数。
		trail = append(trail, attemptResult.Trail...) // 合并当前 Provider 的尝试记录。
		if failure == nil {                           // 当前 Provider 调用成功。
			return ExecutionResult{
				Body:            attemptResult.Body,          // 返回成功响应体。
				ProviderID:      attemptResult.ProviderID,    // 返回成功 Provider。
				UpstreamModel:   attemptResult.UpstreamModel, // 返回真实上游模型。
				KeyID:           attemptResult.KeyID,         // 返回最终 Key ID。
				Attempts:        totalAttempts,               // 返回累计尝试次数。
				Fallbacks:       fallbacks,                   // 返回累计回退次数。
				CandidatesTried: index + 1,                   // 当前下标加一即尝试过的候选数。
				Trail:           trail,                       // 返回完整尝试记录。
			}, nil
		}

		lastFailure = failure                 // 保存当前候选的最终失败。
		failure.TotalAttempts = totalAttempts // 补充全部真实调用次数。
		failure.Fallbacks = fallbacks         // 补充当前已发生的回退次数。
		failure.Trail = trail                 // 补充完整尝试记录。
		if executionContext.Err() != nil {    // 总请求已经超时或被取消。
			failure = FromProvider(executionContext, candidate.ProviderID, candidate.Priority, failure.Attempt, executionContext.Err()) // 将 Context 错误转换为统一 Failure。
			failure.TotalAttempts = totalAttempts                                                                                       // 补充累计尝试次数。
			failure.Fallbacks = fallbacks                                                                                               // 补充累计回退次数。
			failure.Trail = trail                                                                                                       // 补充尝试记录。
			return ExecutionResult{}, failure                                                                                           // 总预算结束，不再尝试其他 Provider。
		}
		if !SignalsFor(failure).Fallback || index == len(input.Plan.Candidates)-1 { // 错误不允许回退或已是最后一个候选。
			return ExecutionResult{}, failure // 直接返回当前失败。
		}
		addFallbackEvent( // 在当前请求 Span 上记录跨 Provider 切换。
			executionContext,
			candidate.ProviderID,
			input.Plan.Candidates[index+1].ProviderID,
			fallbacks+1,
			failure,
		)
		fallbacks++ // 准备切换到下一个候选 Provider。
	}

	if lastFailure == nil { // 理论兜底：循环没有产生失败结果。
		lastFailure = &Failure{Category: CategoryLocalValidation, Cause: routing.ErrNoRoute} // 构造无路由错误。
	}
	lastFailure.TotalAttempts = totalAttempts // 补充最终尝试次数。
	lastFailure.Fallbacks = fallbacks         // 补充最终回退次数。
	lastFailure.Trail = trail                 // 补充最终尝试记录。
	return ExecutionResult{}, lastFailure     // 返回最后一次失败。
}

// OpenStream 在响应尚未发送给客户端前完成候选内重试和 Provider 回退。
// 成功建流后沿用客户端 Context；总请求超时只约束建流和首事件阶段。
func (orchestrator *Orchestrator) OpenStream(
	ctx context.Context, // 客户端请求 Context。
	input ExecutionInput, // 请求内容和路由计划。
) (*PreparedStream, *Failure) {
	if len(input.Plan.Candidates) == 0 { // 路由计划没有候选 Provider。
		return nil, &Failure{
			Category: CategoryLocalValidation, // 归类为本地配置错误。
			Cause:    routing.ErrNoRoute,      // 具体原因是没有路由。
		}
	}

	executionContext, cancelExecution := orchestrator.retries.RequestContext(ctx) // 创建建流和首事件阶段的总超时 Context。
	defer cancelExecution()                                                       // OpenStream 返回时释放建流 Context。

	totalAttempts := 0                                    // 累计所有候选的建流尝试次数。
	fallbacks := 0                                        // 累计流式请求切换 Provider 的次数。
	var trail []AttemptRecord                             // 累计建流尝试记录。
	var lastFailure *Failure                              // 保存最后一次建流失败。
	for index, candidate := range input.Plan.Candidates { // 按顺序尝试候选 Provider。
		attemptResult, failure := orchestrator.attempts.prepareStream( // 在当前 Provider 内重试建流并读取首个事件。
			executionContext, // 控制建流和首事件阶段的总时间。
			ctx,              // 成功后流继续使用客户端原始 Context。
			AttemptInput{
				RequestID:      input.RequestID,           // 传递请求 ID。
				RequestedModel: input.Plan.RequestedModel, // 传递客户端模型名。
				Request:        input.Request,             // 传递流式聊天请求。
				Candidate:      candidate,                 // 传递当前候选 Provider。
			},
		)
		totalAttempts += attemptResult.attempts       // 累加当前 Provider 的建流尝试次数。
		trail = append(trail, attemptResult.trail...) // 合并当前 Provider 的尝试记录。
		if failure == nil {                           // 当前 Provider 成功建流并取得首个事件。
			stream := attemptResult.stream                        // 取得已准备好的流对象。
			stream.Attempts = totalAttempts                       // 写入全部尝试次数。
			stream.Fallbacks = fallbacks                          // 写入全部 Provider 回退次数。
			stream.CandidatesTried = index + 1                    // 写入实际尝试过的候选数量。
			stream.Trail = append([]AttemptRecord(nil), trail...) // 复制尝试记录，避免后续切片修改。
			return stream, nil                                    // 返回已准备好的流。
		}

		lastFailure = failure                                  // 保存当前候选建流失败。
		failure.TotalAttempts = totalAttempts                  // 补充累计尝试次数。
		failure.Fallbacks = fallbacks                          // 补充累计回退次数。
		failure.Trail = append([]AttemptRecord(nil), trail...) // 复制并保存完整尝试记录。
		if executionContext.Err() != nil {                     // 建流总预算已超时或取消。
			failure = FromProvider(
				executionContext,       // 使用已经结束的执行 Context。
				candidate.ProviderID,   // 当前 Provider ID。
				candidate.Priority,     // 当前候选优先级。
				failure.Attempt,        // 当前尝试编号。
				executionContext.Err(), // Context 的超时或取消错误。
			)
			failure.TotalAttempts = totalAttempts                  // 补充累计尝试次数。
			failure.Fallbacks = fallbacks                          // 补充累计回退次数。
			failure.Trail = append([]AttemptRecord(nil), trail...) // 补充尝试记录副本。
			return nil, failure                                    // 总预算结束，不再尝试其他 Provider。
		}
		if !SignalsFor(failure).Fallback || index == len(input.Plan.Candidates)-1 { // 错误不允许回退或已无后续候选。
			return nil, failure // 返回当前建流失败。
		}
		addFallbackEvent( // 在当前请求 Span 上记录首事件前的 Provider 切换。
			executionContext,
			candidate.ProviderID,
			input.Plan.Candidates[index+1].ProviderID,
			fallbacks+1,
			failure,
		)
		fallbacks++ // 准备切换到下一个 Provider 建流。
	}

	if lastFailure == nil { // 理论兜底：没有得到任何失败。
		lastFailure = &Failure{Category: CategoryLocalValidation, Cause: routing.ErrNoRoute} // 构造无路由错误。
	}
	lastFailure.TotalAttempts = totalAttempts                  // 补充最终累计尝试次数。
	lastFailure.Fallbacks = fallbacks                          // 补充最终回退次数。
	lastFailure.Trail = append([]AttemptRecord(nil), trail...) // 补充最终尝试记录副本。
	return nil, lastFailure                                    // 返回最后一次建流失败。
}
