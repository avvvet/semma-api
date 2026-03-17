package handler

import (
	"encoding/json"
	"net/http"

	"github.com/avvvet/semma-api/store"
)

func Recent(w http.ResponseWriter, r *http.Request) {
	items, err := store.GetAll()
	if err != nil {
		writeError(w, "failed to get recent", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}
