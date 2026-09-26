package main

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// mergeDocs 把多份文档合并成一份。
//
// 生成器按 proto 分包产文档，而渲染器一次只吃一份 spec，所以需要合并。
//
// components 的名字是全限定的（aladdin.identity.v1.LoginRequest），跨文件不会
// 撞名。唯一会重的是生成器自带的 Connect 通用 schema（connect.error、
// connect-protocol-version 等），它们在每份文档里都相同——**相同则去重，
// 不同则报错**，不静默取其一：静默取会让某一份文档的 schema 悄悄消失。
func mergeDocs(docs []*yaml.Node) (*yaml.Node, error) {
	merged := &yaml.Node{Kind: yaml.MappingNode}
	appendKV(merged, "openapi", "3.1.0")

	info := &yaml.Node{Kind: yaml.MappingNode}
	appendKV(info, "title", "aladdin API")
	appendKV(info, "version", "v1")
	appendKV(info, "description",
		"由 `make api-docs` 从 proto 生成，请勿手改。\n\n"+
			"每个方法的鉴权要求写在它说明的开头；同一份信息也以 `x-aladdin-auth` "+
			"扩展给出，供机器读取。")
	merged.Content = append(merged.Content, scalar("info"), info)

	paths := &yaml.Node{Kind: yaml.MappingNode}
	schemas := &yaml.Node{Kind: yaml.MappingNode}
	seenSchemas := map[string]*yaml.Node{}
	seenPaths := map[string]bool{}
	tags := &yaml.Node{Kind: yaml.SequenceNode}
	seenTags := map[string]bool{}

	for _, doc := range docs {
		root := rootMapping(doc)

		if p := mappingValue(root, "paths"); p != nil && p.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(p.Content); i += 2 {
				name := p.Content[i].Value
				if seenPaths[name] {
					return nil, fmt.Errorf("合并冲突：路径 %s 在多份文档中重复", name)
				}
				seenPaths[name] = true
				paths.Content = append(paths.Content, p.Content[i], p.Content[i+1])
			}
		}

		if c := mappingValue(root, "components"); c != nil {
			s := mappingValue(c, "schemas")
			if s == nil || s.Kind != yaml.MappingNode {
				continue
			}
			for i := 0; i+1 < len(s.Content); i += 2 {
				name, val := s.Content[i].Value, s.Content[i+1]
				if prev, ok := seenSchemas[name]; ok {
					if !nodeEqual(prev, val) {
						return nil, fmt.Errorf("合并冲突：schema %s 在多份文档中定义不一致", name)
					}
					continue
				}
				seenSchemas[name] = val
				schemas.Content = append(schemas.Content, s.Content[i], s.Content[i+1])
			}
		}

		if t := mappingValue(root, "tags"); t != nil && t.Kind == yaml.SequenceNode {
			for _, tag := range t.Content {
				if name := mappingValue(tag, "name"); name != nil && !seenTags[name.Value] {
					seenTags[name.Value] = true
					tags.Content = append(tags.Content, tag)
				}
			}
		}
	}

	merged.Content = append(merged.Content, scalar("paths"), paths)

	if len(schemas.Content) > 0 {
		components := &yaml.Node{Kind: yaml.MappingNode}
		components.Content = append(components.Content, scalar("schemas"), schemas)
		merged.Content = append(merged.Content, scalar("components"), components)
	}
	if len(tags.Content) > 0 {
		merged.Content = append(merged.Content, scalar("tags"), tags)
	}
	return merged, nil
}

// nodeEqual 判断两个 YAML 节点序列化后是否一致。
func nodeEqual(a, b *yaml.Node) bool {
	ab, errA := yaml.Marshal(a)
	bb, errB := yaml.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ab, bb)
}
