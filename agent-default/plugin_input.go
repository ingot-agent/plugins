package agentdefault

import (
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
)

func (r *runtime) projectPluginInput(entry session.Entry) (model.Message, bool, error) {
	if r.pluginInputs == nil {
		return model.Message{}, false, nil
	}
	return r.pluginInputs.Project(entry)
}
