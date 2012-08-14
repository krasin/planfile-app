define 'planfile', (exports, root) ->

  doc = root.document
  events = {}
  evid = 1
  isArray = Array.isArray

  propFix =
    cellpadding: "cellPadding"
    cellspacing: "cellSpacing"
    class: "className"
    colspan: "colSpan"
    contenteditable: "contentEditable"
    for: "htmlFor"
    frameborder: "frameBorder"
    maxlength: "maxLength",
    readonly: "readOnly"
    rowspan: "rowSpan"
    tabindex: "tabIndex"
    usemap: "useMap"

  buildDOM = (data, parent) ->
    l = data.length
    if l >= 1
      tag = data[0] # TODO(tav): use this to check which attrs are valid.
      elem = doc.createElement tag
      parent.appendChild elem
    if l >= 2
      attrs = data[1]
      start = 1
      if !isArray(attrs) and typeof attrs is 'object'
        for k, v of attrs
          type = typeof v
          if k.lastIndexOf('on', 0) is 0
            if type isnt 'function'
              continue
            if !elem.__evi
              elem.__evi = evid++
            type = k.slice 2
            if events[elem.__evi]
              events[elem.__evi].push [type, v, false]
            else
              events[elem.__evi] = [[type, v, false]]
            elem.addEventListener type, v, false
          else
            elem[propFix[k] or k] = v
        start = 2
      for child in data[start...l]
        type = typeof child
        if type is 'string'
          elem.appendChild document.createTextNode child
        else
          buildDOM child, elem
    return

  exports.domly = (data, target) ->
    frag = doc.createDocumentFragment()
    buildDOM data, frag
    target.appendChild frag
    return

  purgeDOM = (elem) ->
    evi = elem.__evi
    if evi
      for [type, func, capture] in events[evi]
        elem.removeEventListener type, func, capture
      delete events[evi]
    children = elem.childNodes
    if children
      for child in children
        purgeDOM child
    return

  exports.rmtree = (parent, elem) ->
    parent.removeChild elem
    purgeDOM elem
    return

  return
