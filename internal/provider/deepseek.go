package provider

type deepSeekAdapter struct {
	*compatibleChatAdapter
}

func newDeepSeekAdapter(baseURL string, httpConfig HTTPConfig) (*deepSeekAdapter, error) {
	chat, err := newVendorCompatibleChat(ProtocolDeepSeek, baseURL, false, httpConfig)
	if err != nil {
		return nil, err
	}
	return &deepSeekAdapter{compatibleChatAdapter: chat}, nil
}
