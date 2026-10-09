{
	"subject": {{ toJson .Subject }},
	"sans": {{ toJson .Insecure.User.sans }},
{{- if typeIs "*rsa.PublicKey" .Insecure.CR.PublicKey }}
	"keyUsage": ["keyEncipherment", "digitalSignature"],
{{- else }}
	"keyUsage": ["digitalSignature"],
{{- end }}
	"extKeyUsage": ["serverAuth", "clientAuth"]
{{- if .Insecure.User.attributes }},
	"extensions": [{
		"id": "1.3.6.1.4.1.61183.1.1",
		"critical": false,
		"value": "{{ toJson .Insecure.User.attributes | b64enc }}"
	}]
{{- end }}
}
