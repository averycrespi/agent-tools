package main

import (
	"net/http"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
	"github.com/spf13/cobra"
)

func runConfigExport(command *cobra.Command, options *onlineOptions) error {
	client, err := controlclient.New(options.address, controlclient.TransportOptions{})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	header, err := controlclient.RequestMetadata(controlclient.RequestMetadataOptions{Bearer: options.adminBearer.value})
	if err != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(err))
	}
	response, err := client.Do(command.Context(), controlclient.Request{Method: http.MethodGet, Path: contract.ConfigExportPath, Header: header})
	if err != nil {
		return writeOnlineFailure(command, options.output, classifyReadFailure(err))
	}
	if failure := evaluateOnlineResponse(response, options.adminBearer.path); failure != nil {
		return writeOnlineFailure(command, options.output, failure)
	}
	var document contract.ConfigBundle
	if controlclient.DecodeConfigResponse(response.Body, &document) != nil || contract.ValidateConfigBundleEnvelope(document) != nil {
		return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(controlclient.ErrResponseInvalid))
	}
	for _, definition := range document.Resources {
		if definition.Reserved {
			continue
		}
		spec := contract.ConfigSpecDestination(definition.Kind)
		if spec == nil || controlclient.DecodeConfigResponse(definition.Spec, spec) != nil {
			return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(controlclient.ErrResponseInvalid))
		}
		if server, ok := spec.(*contract.ConfigServerSpec); ok && !server.Deleted && validateServerTransportInput(server.Transport) != nil {
			return writeOnlineFailure(command, options.output, controlclient.ClassifyClientError(controlclient.ErrResponseInvalid))
		}
	}
	if err := controlclient.WriteConfigDocument(command.OutOrStdout(), response.Body); err != nil {
		return writeOnlineFailure(command, options.output, controlclient.NewInputError("The configuration output could not be written."))
	}
	return nil
}
