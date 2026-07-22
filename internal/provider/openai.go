package provider

type openAIAdapter struct {
	*compatibleChatAdapter
}

func newOpenAIAdapter(baseURL string, httpConfig HTTPConfig) (*openAIAdapter, error) {
	chat, err := newVendorCompatibleChat(ProtocolOpenAI, baseURL, true, httpConfig)
	if err != nil {
		return nil, err
	}
	return &openAIAdapter{compatibleChatAdapter: chat}, nil
}
