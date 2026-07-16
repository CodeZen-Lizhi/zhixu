---
status: accepted
---

# 核心业务不依赖 LangChain

Agent、RAG、Workflow、Tool Permission 和 Structured Output 是项目核心能力，需要稳定领域 Interface 和可解释执行链。核心模块直接依赖 LangChain 会将业务语义绑定到框架抽象，因此只允许在 Adapter 内复用必要工具，不让框架类型进入领域层。

## Consequences

- 需要实现少量 Model、Retrieval 和 Tool Interface。
- 面试和测试可以直接解释真实工作流，而不是依赖黑盒框架。

