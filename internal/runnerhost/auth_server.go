package runnerhost

import "net/http"

func (s *Server) authStart(writer http.ResponseWriter, request *http.Request) {
	var input authStartRequest
	if !decodeRequest(writer, request, &input) {
		return
	}
	response, err := s.auth.start(request.Context(), input)
	if err != nil {
		response.Error = err.Error()
	}
	writeJSON(writer, response)
}

func (s *Server) authSubmit(writer http.ResponseWriter, request *http.Request) {
	var input authFlowRequest
	if !decodeRequest(writer, request, &input) {
		return
	}
	response := authStatusResponse{}
	if err := s.auth.submit(request.Context(), input); err != nil {
		response.Error = err.Error()
	}
	writeJSON(writer, response)
}

func (s *Server) authStatus(writer http.ResponseWriter, request *http.Request) {
	var input authFlowRequest
	if !decodeRequest(writer, request, &input) {
		return
	}
	response, err := s.auth.status(input.ID)
	if err != nil {
		response.Error = err.Error()
	}
	writeJSON(writer, response)
}

func (s *Server) authCancel(writer http.ResponseWriter, request *http.Request) {
	var input authFlowRequest
	if !decodeRequest(writer, request, &input) {
		return
	}
	response := authStatusResponse{}
	if err := s.auth.cancel(input.ID); err != nil {
		response.Error = err.Error()
	}
	writeJSON(writer, response)
}
