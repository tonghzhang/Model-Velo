package provider

type zhipuAdapter struct {
	*compatibleChatAdapter
}

func newZhipuAdapter(baseURL string, httpConfig HTTPConfig) (*zhipuAdapter, error) {
	chat, err := newVendorCompatibleChat(ProtocolZhipu, baseURL, true, httpConfig)
	if err != nil {
		return nil, err
	}
	return &zhipuAdapter{compatibleChatAdapter: chat}, nil
}
