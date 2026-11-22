import type { ComponentProps } from "react";

// The small elements that pages repeat with the same class, written once. Each takes the props of the element it stands for.

export const BtnLink = (p: ComponentProps<"button">) => <button className="linkbtn" type="button" {...p} />;
export const BtnPrimary = (p: ComponentProps<"button">) => <button className="btn btn-primary" type="submit" {...p} />;
export const BtnSm = (p: ComponentProps<"button">) => <button className="btn btn-sm" type="button" {...p} />;
export const DivActions = (p: ComponentProps<"div">) => <div className="dialog-actions" {...p} />;
export const DivFaintSmall = (p: ComponentProps<"div">) => <div className="faint small" {...p} />;
export const DivLoading = (p: ComponentProps<"div">) => <div className="loading" {...p} />;
export const DivPage = (p: ComponentProps<"div">) => <div className="page" {...p} />;
// The control is the label's child at each use, which the rule cannot see through a component.
// eslint-disable-next-line jsx-a11y/label-has-associated-control
export const LabelField = (p: ComponentProps<"label">) => <label className="field" {...p} />;
export const PSub = (p: ComponentProps<"p">) => <p className="sub" {...p} />;
export const SpanFaint = (p: ComponentProps<"span">) => <span className="faint" {...p} />;
export const SpanFaintSmall = (p: ComponentProps<"span">) => <span className="faint small" {...p} />;
export const SpanHidden = (p: ComponentProps<"span">) => <span className="visually-hidden" {...p} />;
export const SpanMono = (p: ComponentProps<"span">) => <span className="mono" {...p} />;
export const TdNum = (p: ComponentProps<"td">) => <td className="num" {...p} />;
export const ThCol = (p: ComponentProps<"th">) => <th scope="col" {...p} />;
export const ThRow = (p: ComponentProps<"th">) => <th scope="row" {...p} />;
