import { describe, expect, it } from "vitest";
import { parseTerminal, parseDelta } from "./payload";
describe("事件契约", () => {
  it("不把未知终态当成成功", () => {
    expect(parseTerminal({ status: "unknown" })).toBeNull();
  });
  it("保留重启中断语义", () => {
    expect(parseTerminal({ status: "interrupted" })?.status).toBe(
      "interrupted",
    );
  });
  it("不捏造首 token 时间", () => {
    expect(parseTerminal({ status: "completed" })?.ttftMs).toBeUndefined();
  });
  it("只按约定字段读取增量", () => {
    expect(parseDelta({ delta: "你好" })).toBe("你好");
    expect(parseDelta({ content: "错误字段" })).toBe("");
  });
});
