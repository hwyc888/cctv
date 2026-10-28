package server

import "net/http"

func (s *Server) queueSubscriptionEpisodes(w http.ResponseWriter, r *http.Request) {
	var input struct { EpisodeIDs []string `json:"episodeIds"` }
	if err := decodeJSON(r, &input); err != nil { writeError(w, http.StatusBadRequest, err); return }
	added, skipped, err := s.manager.QueueSubscriptionEpisodes(r.PathValue("id"), input.EpisodeIDs)
	if err != nil { writeError(w, http.StatusBadRequest, err); return }
	writeJSON(w, http.StatusOK, map[string]int{"added": added, "skipped": skipped})
}
