package provider

type nvidiaAdapter struct {
	*compatibleChatAdapter
}

func newNVIDIAAdapter(baseURL string, httpConfig HTTPConfig) (*nvidiaAdapter, error) {
	chat, err := newVendorCompatibleChat(ProtocolNVIDIA, baseURL, true, httpConfig)
	if err != nil {
		return nil, err
	}
	return &nvidiaAdapter{compatibleChatAdapter: chat}, nil
}
