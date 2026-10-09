{
	"subject": {{ toJson .Subject }},
	"sans": {{ toJson .SANs }},
{{- if typeIs "*rsa.PublicKey" .Insecure.CR.PublicKey }}
	"keyUsage": ["keyEncipherment", "digitalSignature"],
{{- else }}
	"keyUsage": ["digitalSignature"],
{{- end }}
{{- if eq .Insecure.User.tier "bootstrap" }}
	"extKeyUsage": ["clientAuth"],
	"unknownExtKeyUsage": ["1.3.6.1.4.1.61183.1.3"]
{{- else }}
	"extKeyUsage": ["serverAuth", "clientAuth"]
{{- end }}
{{- if .Insecure.User.attributes }},
	"extensions": [{
		"id": "1.3.6.1.4.1.61183.1.1",
		"critical": false,
		"value": "{{ toJson .Insecure.User.attributes | b64enc }}"
	}]
{{- end }}
}
