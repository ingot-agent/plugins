package editorcmd

import (
	"reflect"
	"testing"
)

func TestExpandPreservesFileArgument(t *testing.T) {
	path := `/tmp/中文 folder/a 'quoted' "file"; $(touch unexpected) ${file_path}.txt`
	for _, test := range []struct {
		name     string
		template string
		want     []string
	}{
		{"unquoted", `editor --before ${file_path} --after`, []string{"editor", "--before", path, "--after"}},
		{"quoted", `code --reuse-window "${file_path}"`, []string{"code", "--reuse-window", path}},
		{"embedded", `editor --file='${file_path}' --again=${file_path}`, []string{"editor", "--file=" + path, "--again=" + path}},
		{"empty argument", `editor "" ${file_path}`, []string{"editor", "", path}},
		{"Windows executable", `"C:\Program Files\Editor\editor.exe" "${file_path}"`, []string{`C:\Program Files\Editor\editor.exe`, path}},
		{"Windows UNC", `'\\server\editors\editor.exe' '${file_path}'`, []string{`\\server\editors\editor.exe`, path}},
		{"escaped space", `/opt/My\ Editor/editor ${file_path}`, []string{"/opt/My Editor/editor", path}},
		{"literal operators", `editor --title="a|b&c;d<e>f" ${file_path}`, []string{"editor", "--title=a|b&c;d<e>f", path}},
		{"empty", " \t ", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := Expand(test.template, path)
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("Expand = %#v, %v; want %#v", got, err, test.want)
			}
		})
	}
}

func TestParseRejectsInvalidCommands(t *testing.T) {
	for _, template := range []string{
		`editor --reuse-window`, `"" ${file_path}`, `${file_path} argument`,
		`editor "${file_path}`, `editor '${file_path}`, `editor ${file_path} ${unknown}`,
		`editor ${file_path} && other`, `editor ${file_path} | other`, `editor ${file_path} > log`,
		"editor ${file_path}\nother", "editor ${file_path}\x00", string([]byte{0xff}),
	} {
		if _, err := Parse(template); err == nil {
			t.Errorf("Parse(%q) accepted an invalid template", template)
		}
	}
}
