import Icon from "./Icon.jsx";

const toneClasses = {
  info: "bg-primary/10 border-primary/30 text-on-surface",
  warn: "bg-tertiary/10 border-tertiary/20 text-on-surface",
  error: "bg-error/10 border-error/20 text-on-surface",
};

const iconNames = {
  info: "info",
  warn: "warning",
  error: "error",
};

const iconColors = {
  info: "text-primary",
  warn: "text-tertiary",
  error: "text-error",
};

export default function InlineNotice({ tone = "error", title, message }) {
  const boxClass = toneClasses[tone] ?? toneClasses.error;
  const iconName = iconNames[tone] ?? iconNames.error;
  const iconColor = iconColors[tone] ?? iconColors.error;

  return (
    <div className={`flex items-start gap-3 rounded-xl border p-4 ${boxClass}`}>
      <Icon name={iconName} className={`mt-0.5 h-5 w-5 shrink-0 ${iconColor}`} />
      <div className="min-w-0">
        {title ? <p className="font-headline text-sm font-bold">{title}</p> : null}
        <p className={title ? "mt-1 text-sm text-on-surface-variant" : "text-sm text-on-surface-variant"}>{message}</p>
      </div>
    </div>
  );
}
