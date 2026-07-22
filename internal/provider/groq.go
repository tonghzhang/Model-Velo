package provider

type groqAdapter struct {
	*compatibleChatAdapter
}

func newGroqAdapter(baseURL string, httpConfig HTTPConfig) (*groqAdapter, error) {
	chat, err := newVendorCompatibleChat(ProtocolGroq, baseURL, true, httpConfig)
	if err != nil {
		return nil, err
	}
	return &groqAdapter{compatibleChatAdapter: chat}, nil
}
