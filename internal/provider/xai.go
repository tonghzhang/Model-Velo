package provider

type xAIAdapter struct {
	*compatibleChatAdapter
}

func newXAIAdapter(baseURL string, httpConfig HTTPConfig) (*xAIAdapter, error) {
	chat, err := newVendorCompatibleChat(ProtocolXAI, baseURL, true, httpConfig)
	if err != nil {
		return nil, err
	}
	return &xAIAdapter{compatibleChatAdapter: chat}, nil
}
