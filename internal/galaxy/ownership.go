package galaxy

import (
	"context"
	"errors"
)

// OwnedProject 读取一个工程，并校验调用者是它的拥有者（归属校验的唯一入口）。
//
// 每个读写动作都要过这一关。它由**凭证**决定，不由请求里的字段决定——接口面
// 上不存在"指定拥有者"这个形状，因此也不存在一个可以被伪造的拥有者参数。
//
// **"不是你的"与"不存在"返回同一个结论**（都是 ErrProjectNotFound）。区分
// 它们等于提供一个工程枚举接口：拿着别人的工程标识反复调用，"不存在"与
// "无权访问"的差集就能把一个不可猜标识试出来。而工程标识是发布地址的一部分，
// 猜出它就能看到别人的页面——这与发布态"地址即凭据"是同一条取向：**地址的
// 可猜性是唯一防线，那就不能有任何东西帮人猜。**
//
// 存储故障**不折叠**成"不存在"：前者是故障，后者是正常状态。混为一谈会把
// 一次数据库抖动表现成"我的工程全没了"，也会让重试策略失去依据。
func OwnedProject(ctx context.Context, store Store, projectID, subjectID string) (Project, error) {
	project, err := store.GetProject(ctx, projectID)
	if err != nil {
		if errors.Is(err, ErrProjectNotFound) {
			return Project{}, ErrProjectNotFound
		}
		return Project{}, err
	}
	if project.OwnerSubjectID != subjectID {
		return Project{}, ErrProjectNotFound
	}
	return project, nil
}
