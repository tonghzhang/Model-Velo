package provider

type mistralAdapter struct {
	*compatibleChatAdapter
}

func newMistralAdapter(baseURL string, httpConfig HTTPConfig) (*mistralAdapter, error) {
	chat, err := newVendorCompatibleChat(ProtocolMistral, baseURL, true, httpConfig)
	if err != nil {
		return nil, err
	}
	return &mistralAdapter{compatibleChatAdapter: chat}, nil
}
