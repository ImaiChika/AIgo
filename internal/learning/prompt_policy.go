package learning

import "aigo/internal/domain"

const legacyReflectionParagraph = "一次响应给出三种表达方案：concise（简洁命题规则）、detailed（带前后例子的经验）、checklist（可勾选清单），每版1—6条有界规则。区分风格、考核任务调整和医学事实变更。一次删去某检查不等于所有题禁止检查；答案改变不证明新答案正确。医学结论或答案变化须标medical_change=true并提示核对，不能凭空补来源、写已验证医学依据，不能修改源题、触发复检、自动启用经验或发布共享卡。\n"

const scopedReflectionParagraph = "一次响应给出三种表达方案：concise（简洁命题规则）、detailed（带前后例子的经验）、checklist（可勾选清单），每版1—6条有界规则。任务是围绕老师实际保存的差异提炼命题经验，区分风格、考核任务调整和医学事实变化；使用必要领域判断识别改动影响与风险，不把本次总结扩展成重新完整解题、诊断裁决或审核结论。\n" +
	"前后例子均依据输入，不补造病例或来源；未描述的检查/病史不等于未实施、未查或阴性。单题修改不推广成所有题的禁令。医学事实或答案变化须标medical_change=true并提示人工核对，老师改后答案不等于已经正确，不写已验证医学依据。\n" +
	"候选只供老师选择编辑，针对题目内容，不制定审核人数/轮数、权限或发布门禁，不把AI新生成格式反向强加给老师已保存的人工修订，也不把本段职责说明当成候选经验。不修改源题、触发复检、自动启用经验或发布共享卡；这些仍由原系统流程和老师选择处理。\n"

func reflectionParagraph(policy string) string {
	if policy == domain.LearningScopedReflectionPrompt {
		return scopedReflectionParagraph
	}
	return legacyReflectionParagraph
}
