package provider

type togetherAdapter struct {
	*compatibleChatAdapter
}

func newTogetherAdapter(baseURL string, httpConfig HTTPConfig) (*togetherAdapter, error) {
	chat, err := newVendorCompatibleChat(ProtocolTogether, baseURL, true, httpConfig)
	if err != nil {
		return nil, err
	}
	return &togetherAdapter{compatibleChatAdapter: chat}, nil
}
